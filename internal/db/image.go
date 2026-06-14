package db

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// Status constants for image processing state.
const (
	StatusDiscovered = "discovered" // found on camera, not yet queued
	StatusQueued     = "queued"     // user queued it, waiting for or between retries
	StatusUploading  = "uploading"  // worker is processing it
	StatusDone       = "done"       // successfully uploaded
	StatusFailed     = "failed"     // failed after max retries
)

// ImageRecord is the GORM model for a camera image.
type ImageRecord struct {
	gorm.Model
	Filename     string     `gorm:"uniqueIndex;not null"`
	URL          string     `gorm:"uniqueIndex;not null"`
	Status       string     `gorm:"not null;default:'discovered';index"`
	RetryCount   int        `gorm:"default:0"`
	LastError    string
	NextRetryAt  *time.Time `gorm:"index"`
	CapturedAt   *time.Time `gorm:"index"` // PTP CaptureDate from camera; nil if not available
	IsVideo      bool       `gorm:"default:false"` // true for MOV/MP4/etc.
	CameraFolder string     `gorm:"default:''"` // DCIM subfolder name (e.g. "100CANON"); empty for legacy records
	DestPath     string     `gorm:"default:''"` // explicit upload destination; overrides pairing when set
	UploadSource string     `gorm:"default:''"` // "auto" | "manual" | "" for legacy/undetermined
}

// ImageRepo handles CRUD operations for images.
type ImageRepo struct {
	db *gorm.DB
}

// NewImageRepo returns a new ImageRepo backed by db.
func NewImageRepo(db *gorm.DB) *ImageRepo {
	return &ImageRepo{db: db}
}

// FindOrCreate inserts a new image record if it doesn't already exist (by URL).
// Returns the record, a created flag (true when newly inserted or reset), an updated flag
// (true when existing fields were backfilled), and any error.
func (r *ImageRepo) FindOrCreate(filename, url, cameraFolder string, capturedAt *time.Time, isVideo bool) (*ImageRecord, bool, bool, error) {
	var rec ImageRecord
	// Use Find (not First) for the URL lookup so GORM does not log a spurious
	// "record not found" warning for every new image — that is an expected condition.
	var urlMatches []ImageRecord
	if err := r.db.Where("url = ?", url).Limit(1).Find(&urlMatches).Error; err != nil {
		return nil, false, false, err
	}
	if len(urlMatches) == 0 {
		// URL not seen before. Check by filename: the camera may have assigned a new
		// handle to an image we already know (e.g. card re-formatted, restart, or the
		// same SD slot reused across power cycles).
		var filenameMatches []ImageRecord
		if err := r.db.Where("filename = ?", filename).Limit(1).Find(&filenameMatches).Error; err != nil {
			return nil, false, false, err
		}
		if len(filenameMatches) > 0 {
			// Same filename, different URL — update the stored URL so future URL
			// lookups resolve correctly, then return the existing record unchanged.
			rec = filenameMatches[0]
			if err := r.db.Model(&rec).Update("url", url).Error; err != nil {
				return nil, false, false, err
			}
			rec.URL = url
			return &rec, false, false, nil
		}
		// Genuinely new — insert.
		rec = ImageRecord{
			Filename:     filename,
			URL:          url,
			Status:       StatusDiscovered,
			CapturedAt:   capturedAt,
			IsVideo:      isVideo,
			CameraFolder: cameraFolder,
		}
		if err := r.db.Create(&rec).Error; err != nil {
			return nil, false, false, err
		}
		return &rec, true, false, nil
	}
	rec = urlMatches[0]
	if rec.ID == 0 {
		return nil, false, false, nil
	}
	// If the camera reuses a handle/URL (common after DeleteObject), treat it as a new image.
	// We only reset terminal states (done/failed) to avoid interfering with images that are
	// currently queued or being uploaded on the same URL.
	if (rec.Status == StatusDone || rec.Status == StatusFailed) && rec.Filename != filename {
		updates := map[string]interface{}{
			"filename":      filename,
			"status":        StatusDiscovered,
			"retry_count":   0,
			"last_error":    "",
			"next_retry_at": nil,
			"captured_at":   capturedAt,
			"is_video":      isVideo,
		}
		if err := r.db.Model(&rec).Updates(updates).Error; err != nil {
			return nil, false, false, err
		}
		rec.Filename = filename
		rec.Status = StatusDiscovered
		rec.RetryCount = 0
		rec.LastError = ""
		rec.NextRetryAt = nil
		rec.CapturedAt = capturedAt
		rec.IsVideo = isVideo
		return &rec, true, false, nil
	}
	// Backfill fields that are now known but were missing on the existing record.
	updates := map[string]interface{}{}
	if capturedAt != nil && rec.CapturedAt == nil {
		updates["captured_at"] = capturedAt
	}
	if isVideo && !rec.IsVideo {
		updates["is_video"] = true
	}
	if cameraFolder != "" && rec.CameraFolder == "" {
		updates["camera_folder"] = cameraFolder
	}
	if len(updates) > 0 {
		if err := r.db.Model(&rec).Updates(updates).Error; err != nil {
			return nil, false, false, err
		}
		if capturedAt != nil {
			rec.CapturedAt = capturedAt
		}
		if isVideo {
			rec.IsVideo = true
		}
		if cameraFolder != "" {
			rec.CameraFolder = cameraFolder
		}
		return &rec, false, true, nil
	}
	return &rec, false, false, nil
}

// List returns all images in insertion order.
func (r *ImageRepo) List() ([]ImageRecord, error) {
	var recs []ImageRecord
	err := r.db.Order("created_at asc").Find(&recs).Error
	return recs, err
}

// ListByStatus returns all images matching one of the given statuses.
func (r *ImageRepo) ListByStatus(statuses ...string) ([]ImageRecord, error) {
	var recs []ImageRecord
	err := r.db.Where("status IN ?", statuses).Order("created_at asc").Find(&recs).Error
	return recs, err
}

// ListReadyToRetry returns queued images that have exhausted their back-off wait.
func (r *ImageRepo) ListReadyToRetry() ([]ImageRecord, error) {
	var recs []ImageRecord
	err := r.db.
		Where("status = ? AND retry_count > 0 AND (next_retry_at IS NULL OR next_retry_at <= ?)",
			StatusQueued, time.Now()).
		Find(&recs).Error
	return recs, err
}

// ListFreshQueued returns queued images that have never been attempted (retry_count=0).
func (r *ImageRepo) ListFreshQueued() ([]ImageRecord, error) {
	var recs []ImageRecord
	err := r.db.Where("status = ? AND retry_count = 0", StatusQueued).Find(&recs).Error
	return recs, err
}

func (r *ImageRepo) GetByURL(url string) (*ImageRecord, error) {
	var rec ImageRecord
	err := r.db.Where("url = ?", url).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &rec, err
}

func (r *ImageRepo) GetByFilename(filename string) (*ImageRecord, error) {
	var rec ImageRecord
	err := r.db.Where("filename = ?", filename).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &rec, err
}

// SetStatus updates the status and error message for the image identified by URL.
func (r *ImageRepo) SetStatus(url, status, errMsg string) error {
	updates := map[string]interface{}{
		"status":     status,
		"last_error": errMsg,
	}
	if status == StatusDone || status == StatusFailed {
		updates["next_retry_at"] = nil
	}
	return r.db.Model(&ImageRecord{}).Where("url = ?", url).Updates(updates).Error
}

// SetRetryQueued marks an image for retry after a back-off delay.
func (r *ImageRepo) SetRetryQueued(url string, retryCount int, nextRetryAt time.Time, errMsg string) error {
	return r.db.Model(&ImageRecord{}).Where("url = ?", url).Updates(map[string]interface{}{
		"status":       StatusQueued,
		"retry_count":  retryCount,
		"next_retry_at": nextRetryAt,
		"last_error":   errMsg,
	}).Error
}

// MarkQueued transitions discovered images (by URL list) to queued status.
func (r *ImageRepo) MarkQueued(urls []string) error {
	return r.db.Model(&ImageRecord{}).
		Where("url IN ? AND status = ?", urls, StatusDiscovered).
		Updates(map[string]interface{}{
			"status":      StatusQueued,
			"retry_count": 0,
			"last_error":  "",
		}).Error
}

// MarkQueuedWithMeta transitions discovered images to queued and records an
// explicit destination path and upload source ("auto" or "manual").
// Each URL in urls must belong to a discovered image; others are silently skipped.
func (r *ImageRepo) MarkQueuedWithMeta(urls []string, destPath, uploadSource string) error {
	updates := map[string]interface{}{
		"status":        StatusQueued,
		"retry_count":   0,
		"last_error":    "",
		"dest_path":     destPath,
		"upload_source": uploadSource,
	}
	return r.db.Model(&ImageRecord{}).
		Where("url IN ? AND status = ?", urls, StatusDiscovered).
		Updates(updates).Error
}

// MarkAllDiscoveredQueued queues every image that is still in "discovered" status.
// All records are tagged with upload_source="manual" since this is always a user-initiated action.
func (r *ImageRepo) MarkAllDiscoveredQueued() (int64, error) {
	result := r.db.Model(&ImageRecord{}).
		Where("status = ?", StatusDiscovered).
		Updates(map[string]interface{}{
			"status":        StatusQueued,
			"retry_count":   0,
			"last_error":    "",
			"upload_source": "manual",
		})
	return result.RowsAffected, result.Error
}

// ResetQueued resets all images in "queued" status back to "discovered".
// Used by ClearQueue to cancel pending uploads that are not yet in the worker channel.
// Returns the number of records updated.
func (r *ImageRepo) ResetQueued() (int64, error) {
	result := r.db.Model(&ImageRecord{}).
		Where("status = ?", StatusQueued).
		Updates(map[string]interface{}{
			"status":        StatusDiscovered,
			"retry_count":   0,
			"last_error":    "",
			"next_retry_at": nil,
		})
	return result.RowsAffected, result.Error
}

// ResetStuckUploading resets images stuck in "uploading" (interrupted by restart) to "queued".
func (r *ImageRepo) ResetStuckUploading() error {
	return r.db.Model(&ImageRecord{}).
		Where("status = ?", StatusUploading).
		Updates(map[string]interface{}{
			"status":     StatusQueued,
			"last_error": "interrupted by restart",
		}).Error
}

// ResetFailed resets all failed images to queued for re-processing.
// Returns the number of records updated.
func (r *ImageRepo) ResetFailed() (int64, error) {
	result := r.db.Model(&ImageRecord{}).
		Where("status = ?", StatusFailed).
		Updates(map[string]interface{}{
			"status":       StatusQueued,
			"retry_count":  0,
			"last_error":   "",
			"next_retry_at": nil,
		})
	return result.RowsAffected, result.Error
}

// Counts returns a map of status → count.
func (r *ImageRepo) Counts() (map[string]int, error) {
	type row struct {
		Status string
		Count  int
	}
	var rows []row
	err := r.db.Model(&ImageRecord{}).
		Select("status, count(*) as count").
		Group("status").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	m := make(map[string]int, len(rows))
	for _, r := range rows {
		m[r.Status] = r.Count
	}
	return m, nil
}
