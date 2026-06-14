package db

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// ErrPairingNotFound is returned by Delete when no pairing with the given ID exists.
var ErrPairingNotFound = errors.New("pairing not found")

// ErrPairingAlreadyExists is returned by Create when a pairing for that camera
// folder is already configured.
var ErrPairingAlreadyExists = errors.New("a pairing for this camera folder already exists")

// FolderPairingRecord maps a camera folder (DCIM subfolder name, e.g. "100CANON")
// to a destination path on the file share (e.g. "/photos/holidays").
// When a pairing exists, images found inside that camera folder are uploaded
// to the corresponding destination path instead of the global upload path.
// When AutoUpload is true, newly discovered images in that folder are queued
// automatically without any user interaction.
type FolderPairingRecord struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	CameraFolder string `gorm:"not null;uniqueIndex"`
	SharePath    string `gorm:"not null"`
	AutoUpload   bool   `gorm:"default:false"`
}

// FolderPairingRepo handles CRUD operations for folder pairings.
type FolderPairingRepo struct {
	db *gorm.DB
}

// NewFolderPairingRepo returns a new FolderPairingRepo backed by db.
func NewFolderPairingRepo(db *gorm.DB) *FolderPairingRepo {
	return &FolderPairingRepo{db: db}
}

// List returns all folder pairings.
func (r *FolderPairingRepo) List() ([]FolderPairingRecord, error) {
	var records []FolderPairingRecord
	if err := r.db.Order("camera_folder").Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// Create inserts a new pairing.
// Returns ErrPairingAlreadyExists if a pairing for cameraFolder already exists.
func (r *FolderPairingRepo) Create(cameraFolder, sharePath string) (FolderPairingRecord, error) {
	rec := FolderPairingRecord{CameraFolder: cameraFolder, SharePath: sharePath}
	if err := r.db.Create(&rec).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return FolderPairingRecord{}, ErrPairingAlreadyExists
		}
		return FolderPairingRecord{}, err
	}
	return rec, nil
}

// Delete removes the pairing with the given ID.
// Returns ErrPairingNotFound if no row with that ID exists.
func (r *FolderPairingRepo) Delete(id uint) error {
	result := r.db.Delete(&FolderPairingRecord{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPairingNotFound
	}
	return nil
}

// SetAutoUpload toggles the auto_upload flag on an existing pairing.
func (r *FolderPairingRepo) SetAutoUpload(id uint, enabled bool) error {
	result := r.db.Model(&FolderPairingRecord{}).Where("id = ?", id).Update("auto_upload", enabled)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPairingNotFound
	}
	return nil
}

// FindByFolder returns the pairing for a given camera folder name, if any.
func (r *FolderPairingRepo) FindByFolder(cameraFolder string) (FolderPairingRecord, bool) {
	var rec FolderPairingRecord
	if err := r.db.Where("camera_folder = ?", cameraFolder).First(&rec).Error; err != nil {
		return FolderPairingRecord{}, false
	}
	return rec, true
}
