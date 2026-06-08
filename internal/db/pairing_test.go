package db

import (
	"errors"
	"testing"
)

func newTestPairingRepo(t *testing.T) *FolderPairingRepo {
	t.Helper()
	gdb, err := Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return NewFolderPairingRepo(gdb)
}

func TestFolderPairingRepo_CreateAndList(t *testing.T) {
	t.Parallel()
	repo := newTestPairingRepo(t)

	r1, err := repo.Create("100CANON", "/photos/holidays")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if r1.ID == 0 {
		t.Error("expected non-zero ID after Create")
	}
	if r1.CameraFolder != "100CANON" || r1.SharePath != "/photos/holidays" {
		t.Errorf("unexpected record: %+v", r1)
	}

	_, err = repo.Create("101EOS", "/photos/2024")
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}

	list, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 records, got %d", len(list))
	}
	// List is ordered by camera_folder; "100CANON" < "101EOS".
	if list[0].CameraFolder != "100CANON" {
		t.Errorf("expected 100CANON first, got %q", list[0].CameraFolder)
	}
}

func TestFolderPairingRepo_ListEmpty(t *testing.T) {
	t.Parallel()
	repo := newTestPairingRepo(t)
	list, err := repo.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d records", len(list))
	}
}

func TestFolderPairingRepo_FindByFolder(t *testing.T) {
	t.Parallel()
	repo := newTestPairingRepo(t)

	_, err := repo.Create("100CANON", "/photos")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec, ok := repo.FindByFolder("100CANON")
	if !ok {
		t.Fatal("expected FindByFolder to succeed")
	}
	if rec.SharePath != "/photos" {
		t.Errorf("unexpected SharePath: %q", rec.SharePath)
	}

	_, ok = repo.FindByFolder("NOTEXIST")
	if ok {
		t.Error("expected FindByFolder to return false for unknown folder")
	}
}

func TestFolderPairingRepo_Delete(t *testing.T) {
	t.Parallel()
	repo := newTestPairingRepo(t)

	rec, err := repo.Create("100CANON", "/photos")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(rec.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	list, _ := repo.List()
	if len(list) != 0 {
		t.Errorf("expected empty list after delete, got %d", len(list))
	}
}

func TestFolderPairingRepo_DeleteNotFound(t *testing.T) {
	t.Parallel()
	repo := newTestPairingRepo(t)

	err := repo.Delete(9999)
	if !errors.Is(err, ErrPairingNotFound) {
		t.Errorf("expected ErrPairingNotFound, got %v", err)
	}
}

func TestFolderPairingRepo_UniqueConstraint(t *testing.T) {
	t.Parallel()
	repo := newTestPairingRepo(t)

	_, err := repo.Create("100CANON", "/photos/a")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	_, err = repo.Create("100CANON", "/photos/b")
	if !errors.Is(err, ErrPairingAlreadyExists) {
		t.Errorf("expected ErrPairingAlreadyExists for duplicate camera_folder, got %v", err)
	}
}
