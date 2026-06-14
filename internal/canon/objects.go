package canon

// objects.go — PTP object enumeration: traversing storage IDs, folder hierarchies,
// and retrieving object metadata and data. Knows about PTP object semantics but
// not about connection setup or wire framing.

import (
	"context"
	"fmt"
	"strings"

	"github.com/pacorreia/canon-proxy/internal/logger"
)

// discoverDCIMFolders finds DCIM subfolders (e.g. "100CANON") in a single storage volume.
//
// Strategy:
//  1. Enumerate handles at the storage root and call GetObjectInfo on each, skipping on any
//     error. DCIM and its subfolders respond correctly regardless of handle type. Only image-
//     container virtual handles that crash on GetObjectInfo will return an error — those are
//     safely skipped by the err != nil guard.
//  2. Once the DCIM handle is found, enumerate its direct children. Call GetObjectInfo on
//     each child to collect subfolder names (e.g. "100CANON").
//  3. Fallback: if no DCIM is found (e.g. some virtual-only cameras), probe virtual-handle
//     containers two levels deep to find subfolder containers, and try GetObjectInfo on them.
func (c *Client) discoverDCIMFolders(ctx context.Context, storageID uint32) ([]CameraFolder, error) {
	rootHandles, err := c.getObjectHandles(ctx, storageID, 0, 0xFFFFFFFF)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var folders []CameraFolder

	// ---- Pass 1: scan root handles for DCIM, then its children for subfolder names ----
	// Call GetObjectInfo on all handles — folder handles (DCIM, 100CANON, etc.) respond
	// correctly. Handles that would cause a connection reset return an error that we skip.
	var dcimHandle uint32
	for _, h := range rootHandles {
		info, err := c.getObjectInfo(ctx, h)
		if err != nil {
			continue // connection disruption or unsupported handle — skip safely
		}
		if info.format == 0x3001 && strings.EqualFold(info.filename, "DCIM") {
			dcimHandle = h
			break
		}
	}

	if dcimHandle != 0 {
		childHandles, err := c.getObjectHandles(ctx, storageID, 0, dcimHandle)
		if err != nil {
			logger.Warn("component=canon msg=\"discoverDCIMFolders: getObjectHandles(DCIM) failed\" err=%q", err)
		}
		for _, h := range childHandles {
			info, err := c.getObjectInfo(ctx, h)
			if err != nil || info.filename == "" || info.format != 0x3001 {
				continue
			}
			if strings.EqualFold(info.filename, "DCIM") {
				continue
			}
			if seen[info.filename] {
				continue
			}
			seen[info.filename] = true
			folders = append(folders, CameraFolder{Name: info.filename, Handle: h})
		}
		if len(folders) > 0 {
			logger.Info("component=canon msg=\"discovered DCIM folders\" count=%d", len(folders))
			return folders, nil
		}
	}

	// ---- Pass 2: virtual-hierarchy probe ----
	// No named DCIM subfolders found. Look for virtual containers at the root that act as
	// subfolder containers (they have children that are leaf image handles).
	logger.Debug("component=canon msg=\"discoverDCIMFolders: no named DCIM subfolders; probing virtual hierarchy\"")

	for _, rootVirt := range rootHandles {
		if rootVirt < 0x80000000 {
			continue
		}
		level1, err := c.getObjectHandles(ctx, storageID, 0, rootVirt)
		if err != nil || len(level1) == 0 {
			continue
		}
		for _, subVirt := range level1 {
			if subVirt < 0x80000000 {
				continue
			}
			level2, err := c.getObjectHandles(ctx, storageID, 0, subVirt)
			if err != nil || len(level2) == 0 {
				continue
			}
			hasLeaf := false
			for _, leaf := range level2 {
				ch, err := c.getObjectHandles(ctx, storageID, 0, leaf)
				if err == nil && len(ch) == 0 {
					hasLeaf = true
					break
				}
			}
			if !hasLeaf {
				continue
			}
			name := ""
			info, err := c.getObjectInfo(ctx, subVirt)
			if err == nil && info.format == 0x3001 && info.filename != "" {
				name = info.filename
			}
			if name == "" {
				name = fmt.Sprintf("folder%d", len(folders)+1)
			}
			if !seen[name] {
				seen[name] = true
				folders = append(folders, CameraFolder{Name: name, Handle: subVirt})
			}
		}
	}

	if len(folders) > 0 {
		logger.Info("component=canon msg=\"discovered DCIM folders (virtual fallback)\" count=%d", len(folders))
	} else {
		logger.Info("component=canon msg=\"discoverDCIMFolders: no folders found; will enumerate from root\"")
	}
	return folders, nil
}

// listImages enumerates all images across all storage IDs.
//
// It first discovers DCIM subfolders per storage volume so that each image is tagged
// with the correct CameraFolder name. Images that cannot be attributed to a known folder
// (e.g. virtual-handle cameras that expose no DCIM hierarchy) are enumerated from the
// root and tagged with an empty CameraFolder.
//
// knownImageHandles: handles already known to be images — skip GetObjectInfo (pass nil for full scan).
// newFolderHandles:  caller-supplied map that receives any newly-discovered folder handles (may be nil).
func (c *Client) listImages(ctx context.Context, knownImageHandles map[uint32]struct{}, newFolderHandles map[uint32]struct{}) ([]Image, error) {
	// Get storage IDs first (required before GetObjectHandles on many cameras).
	storageIDs, err := c.getStorageIDs(ctx)
	if err != nil {
		logger.Warn("component=canon msg=\"GetStorageIDs failed, falling back to 0xFFFFFFFF\" err=%q", err)
		storageIDs = []uint32{0xFFFFFFFF}
	}
	logger.Debug("component=canon msg=\"storage IDs\" ids=%v", storageIDs)

	var images []Image
	for _, sid := range storageIDs {
		// Step A: discover DCIM subfolders so images can be tagged with a folder name.
		folders, err := c.discoverDCIMFolders(ctx, sid)
		if err != nil {
			logger.Warn("component=canon msg=\"discoverDCIMFolders failed\" storageID=0x%08X err=%q", sid, err)
		}

		if len(folders) > 0 {
			// Build a handle→name map for folder attribution in the virtual hierarchy.
			folderByHandle := make(map[uint32]string, len(folders))
			for _, f := range folders {
				folderByHandle[f.Handle] = f.Name
			}

			// Step B: enumerate images per folder so each image gets a reliable folder tag.
			for _, folder := range folders {
				imgs, err := c.enumerateObjects(ctx, sid, folder.Handle, folder.Name, folderByHandle, knownImageHandles, newFolderHandles)
				if err != nil {
					logger.Warn("component=canon msg=\"enumerate failed\" folder=%q storageID=0x%08X err=%q", folder.Name, sid, err)
					return nil, err
				}
				logger.Info("component=canon msg=\"folder scanned\" folder=%q new_images=%d", folder.Name, len(imgs))
				images = append(images, imgs...)
			}

			if len(images) == 0 && len(knownImageHandles) == 0 {
				// All per-folder scans returned 0 images on the initial full scan.
				// This camera (Canon EOS in WiFi/smartphone mode) likely exposes image data
				// only through the virtual handle tree (handles >= 0x80000000), while the
				// real DCIM folder handles only describe the directory structure.
				// Fall back to a full root enumeration which will traverse virtual containers.
				//
				// Note: in delta mode (knownImageHandles non-empty) a result of 0 new images
				// simply means nothing new has been added — the fallback is not needed and
				// would only cause unnecessary PTP round-trips.
				logger.Info("component=canon msg=\"per-folder scans empty; falling back to root enumeration for virtual handles\"")
				imgs, err := c.enumerateObjects(ctx, sid, 0xFFFFFFFF, "", folderByHandle, knownImageHandles, newFolderHandles)
				if err != nil {
					logger.Warn("component=canon msg=\"root enumerate failed\" storageID=0x%08X err=%q", sid, err)
					return nil, err
				}
				if len(imgs) > 0 {
					logger.Info("component=canon msg=\"root enumeration found images\" count=%d", len(imgs))
					images = append(images, imgs...)
				}
			}
		} else {
			// Fallback: no named DCIM subfolders found (virtual-handle cameras or empty card).
			// Enumerate everything from the root without folder attribution.
			imgs, err := c.enumerateObjects(ctx, sid, 0xFFFFFFFF, "", nil, knownImageHandles, newFolderHandles)
			if err != nil {
				logger.Warn("component=canon msg=\"enumerate failed\" storageID=0x%08X err=%q", sid, err)
				return nil, err
			}
			images = append(images, imgs...)
		}
	}
	return images, nil
}

// enumerateObjects recursively enumerates PTP objects under parent in the given storage.
// Canon EOS cameras in Smartphone mode expose virtual container handles with bit 31 set.
// There are two kinds:
//   - Container handles (e.g. 0x90000000, 0x91900000): GetObjectHandles returns children.
//   - Leaf image handles (e.g. 0x9190XXXX): GetObjectHandles returns count=0; these are
//     the actual images and must be accessed directly via GetObjectInfo/GetObject.
//
// folderName:        the DCIM subfolder name (e.g. "100CANON") already known for this subtree.
//                    Always passed through unchanged; the caller sets it before the first call.
// knownImageHandles: skip GetObjectInfo (and emission) for these handles — already known.
// newFolderHandles:  if non-nil, newly-found folder handles are added to this map.
func (c *Client) enumerateObjects(ctx context.Context, storageID, parent uint32, folderName string, folderByHandle map[uint32]string, knownImageHandles map[uint32]struct{}, newFolderHandles map[uint32]struct{}) ([]Image, error) {
	return c.enumerateObjectsDepth(ctx, storageID, parent, 0, folderName, folderByHandle, knownImageHandles, newFolderHandles)
}

func (c *Client) enumerateObjectsDepth(ctx context.Context, storageID, parent uint32, depth int, folderName string, folderByHandle map[uint32]string, knownImageHandles map[uint32]struct{}, newFolderHandles map[uint32]struct{}) ([]Image, error) {
	if depth > 8 {
		return nil, nil
	}

	handles, err := c.getObjectHandles(ctx, storageID, 0, parent)
	if err != nil {
		return nil, err
	}
	logger.Debug("component=canon msg=\"got handles\" storageID=0x%08X parent=0x%08X depth=%d count=%d", storageID, parent, depth, len(handles))

	var images []Image
	for _, handle := range handles {
		if handle >= 0x80000000 {
			// Virtual handle: check if it has children (container) or is a leaf (image).
			// Avoid calling GetObjectInfo on virtual handles — the camera resets the
			// connection when GetObjectInfo is called on top-level virtual containers.

			// Known image: skip everything.
			if knownImageHandles != nil {
				if _, known := knownImageHandles[handle]; known {
					continue
				}
			}

			children, err := c.getObjectHandles(ctx, storageID, 0, handle)
			if err != nil {
				return nil, err
			}
			if len(children) > 0 {
				// Container: recurse using this handle as the new parent.
				logger.Debug("component=canon msg=\"virtual container\" handle=0x%08X children=%d", handle, len(children))
				if newFolderHandles != nil {
					newFolderHandles[handle] = struct{}{}
				}
				imgs, err := c.enumerateObjectsDepth(ctx, storageID, handle, depth+1, folderName, folderByHandle, knownImageHandles, newFolderHandles)
				if err != nil {
					return nil, err
				}
				images = append(images, imgs...)
			} else {
				// Leaf virtual handle — the actual image object. Try GetObjectInfo.
				logger.Debug("component=canon msg=\"virtual leaf, trying GetObjectInfo\" handle=0x%08X", handle)
				info, err := c.getObjectInfo(ctx, handle)
				if err != nil {
					logger.Warn("component=canon msg=\"virtual leaf GetObjectInfo failed\" handle=0x%08X err=%q", handle, err)
					return nil, err
				}
				if isImageFormat(info.format) {
					logger.Debug("component=canon msg=\"found image\" handle=0x%08X filename=%q", handle, info.filename)
					// Use the caller-provided folderName; if empty, try to resolve from the
					// image's reported parent handle (available when folderByHandle is provided).
					attributedFolder := folderName
					if attributedFolder == "" && folderByHandle != nil {
						if name, ok := folderByHandle[info.parentHandle]; ok {
							attributedFolder = name
						}
					}
					img := Image{
						Handle:       handle,
						URL:          c.handleURL(handle),
						Filename:     info.filename,
						IsVideo:      isVideoFilename(info.filename),
						CameraFolder: attributedFolder,
					}
					if !info.captureDate.IsZero() {
						t := info.captureDate
						img.CapturedAt = &t
					}
					images = append(images, img)
				}
			}
			continue
		}

		// Regular (non-virtual) handle.

		// Already known as an image: skip.
		if knownImageHandles != nil {
			if _, known := knownImageHandles[handle]; known {
				continue
			}
		}

		info, err := c.getObjectInfo(ctx, handle)
		if err != nil {
			logger.Warn("component=canon msg=\"GetObjectInfo failed\" handle=0x%08X err=%q", handle, err)
			return nil, err
		}
		switch {
		case info.format == 0x3001: // Association/folder — recurse
			if newFolderHandles != nil {
				newFolderHandles[handle] = struct{}{}
			}
			imgs, err := c.enumerateObjectsDepth(ctx, storageID, handle, depth+1, folderName, folderByHandle, knownImageHandles, newFolderHandles)
			if err != nil {
				return nil, err
			}
			images = append(images, imgs...)
		case isImageFormat(info.format):
			img := Image{
				Handle:       handle,
				URL:          c.handleURL(handle),
				Filename:     info.filename,
				IsVideo:      isVideoFilename(info.filename),
				CameraFolder: folderName,
			}
			if !info.captureDate.IsZero() {
				t := info.captureDate
				img.CapturedAt = &t
			}
			images = append(images, img)
		default:
			logger.Debug("component=canon msg=\"skipping non-image\" handle=0x%08X format=0x%04X", handle, info.format)
		}
	}
	return images, nil
}

func (c *Client) getStorageIDs(ctx context.Context) ([]uint32, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCGetStorageIDs, txID, false); err != nil {
		return nil, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return nil, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return nil, err
	}
	return parsePTPUint32Array(data), nil
}

func (c *Client) getObjectHandles(ctx context.Context, storageID, format, parent uint32) ([]uint32, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCGetObjectHandles, txID, false, storageID, format, parent); err != nil {
		return nil, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return nil, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return nil, err
	}
	return parsePTPUint32Array(data), nil
}

func (c *Client) getObjectInfo(ctx context.Context, handle uint32) (ptpObjectInfo, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(ptpOCGetObjectInfo, txID, false, handle); err != nil {
		return ptpObjectInfo{}, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return ptpObjectInfo{}, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return ptpObjectInfo{}, err
	}
	return parseObjectInfo(data), nil
}

func (c *Client) getObjectData(ctx context.Context, opcode uint16, handle uint32) ([]byte, error) {
	txID := c.nextTxID()
	if err := c.sendCmdRequest(opcode, txID, false, handle); err != nil {
		return nil, err
	}
	data, err := c.recvData(ctx, txID)
	if err != nil {
		return nil, err
	}
	if _, err := c.recvResponse(ctx, txID); err != nil {
		return nil, err
	}
	return data, nil
}
