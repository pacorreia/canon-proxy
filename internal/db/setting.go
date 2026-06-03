package db

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SettingRecord is the GORM model for a key-value configuration entry.
type SettingRecord struct {
	Key   string `gorm:"primaryKey"`
	Value string
}

// SettingRepo handles CRUD operations for settings.
type SettingRepo struct {
	db *gorm.DB
}

// NewSettingRepo returns a new SettingRepo backed by db.
func NewSettingRepo(db *gorm.DB) *SettingRepo {
	return &SettingRepo{db: db}
}

// Get returns the value for the given key.
func (r *SettingRepo) Get(key string) (string, bool) {
	var s SettingRecord
	if err := r.db.First(&s, "key = ?", key).Error; err != nil {
		return "", false
	}
	return s.Value, true
}

// Set upserts a single key-value pair.
func (r *SettingRepo) Set(key, value string) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&SettingRecord{Key: key, Value: value}).Error
}

// All returns every setting as a map.
func (r *SettingRepo) All() (map[string]string, error) {
	var records []SettingRecord
	if err := r.db.Find(&records).Error; err != nil {
		return nil, err
	}
	m := make(map[string]string, len(records))
	for _, s := range records {
		m[s.Key] = s.Value
	}
	return m, nil
}

// SetMany upserts multiple key-value pairs in a single operation.
func (r *SettingRepo) SetMany(kv map[string]string) error {
	if len(kv) == 0 {
		return nil
	}
	records := make([]SettingRecord, 0, len(kv))
	for k, v := range kv {
		records = append(records, SettingRecord{Key: k, Value: v})
	}
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&records).Error
}

// SeedDefaults inserts defaults for keys that do not yet exist in the database.
func (r *SettingRepo) SeedDefaults(defaults map[string]string) error {
	for k, v := range defaults {
		if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&SettingRecord{Key: k, Value: v}).Error; err != nil {
			return err
		}
	}
	return nil
}
