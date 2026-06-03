package db

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/pacorreia/canon-proxy/internal/logger"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Open initialises the database connection and runs auto-migration.
// driver must be one of "sqlite" (default), "postgres", or "mssql".
func Open(driver, dsn string) (*gorm.DB, error) {
	var dialector gorm.Dialector
	switch strings.ToLower(driver) {
	case "sqlite", "":
		dialector = sqlite.Open(dsn)
	case "postgres":
		dialector = postgres.Open(dsn)
	case "mssql", "sqlserver":
		dialector = sqlserver.Open(dsn)
	default:
		return nil, fmt.Errorf("unsupported database driver: %q", driver)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.AutoMigrate(&ImageRecord{}, &SettingRecord{}); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	logger.Info("component=db msg=\"database ready\" driver=%q", driver)
	return db, nil
}
