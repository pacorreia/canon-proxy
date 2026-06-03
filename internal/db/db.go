package db

import (
	"fmt"
	"log"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.AutoMigrate(&ImageRecord{}, &SettingRecord{}); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	log.Printf("level=info component=db msg=\"database ready\" driver=%q", driver)
	return db, nil
}
