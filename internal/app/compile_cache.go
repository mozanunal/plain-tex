package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// projectCacheDir holds a project's private compiler caches. It sits in the
// build directory, so the file list and Git skip it and it is deleted with the
// project.
const projectCacheDir = buildDirName + "/cache"

// projectCacheEnv seeds the project's private Tectonic and Typst caches from
// the shared ones and returns the environment that points the compilers there.
//
// A sandboxed compile cannot write to the shared caches: a document can write
// any file its compile reaches, so it could plant a poisoned package that then
// runs in every other project's compile. A read-only shared cache does not work
// either, because Tectonic writes every package it downloads, and its bundle
// bookkeeping, into the cache. So each project gets its own cache, seeded with
// symlinks to the shared files. It costs almost no disk, downloads still work,
// and writing through a seeded symlink lands in the read-only shared cache,
// which the sandbox refuses.
func (c *Compiler) projectCacheEnv(projectDir string) ([]string, error) {
	cacheRoot := filepath.Join(projectDir, filepath.FromSlash(projectCacheDir))
	if err := ensureRealDir(filepath.Dir(cacheRoot)); err != nil {
		return nil, err
	}
	if err := ensureRealDir(cacheRoot); err != nil {
		return nil, err
	}

	tectonicDir := filepath.Join(cacheRoot, "tectonic")
	typstDir := filepath.Join(cacheRoot, "typst")
	if err := mirrorCache(c.sandbox.TectonicCacheDir, tectonicDir); err != nil {
		return nil, fmt.Errorf("prepare Tectonic cache: %w", err)
	}
	if err := mirrorCache(c.sandbox.TypstPackageCacheDir, typstDir); err != nil {
		return nil, fmt.Errorf("prepare Typst cache: %w", err)
	}
	return []string{"TECTONIC_CACHE_DIR=" + tectonicDir, "TYPST_PACKAGE_CACHE_PATH=" + typstDir}, nil
}

// mirrorCache gives dst a symlink to every file in shared that dst lacks, so a
// project picks up packages added to the shared cache later. Tectonic rewrites
// its bundle bookkeeping files in place, so those are copied instead; through a
// symlink that write would hit the read-only shared cache and fail the compile.
func mirrorCache(shared, dst string) error {
	if err := ensureRealDir(dst); err != nil {
		return err
	}
	if shared == "" {
		return nil
	}
	if _, err := os.Stat(shared); errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return filepath.WalkDir(shared, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(shared, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if entry.IsDir() {
			return ensureRealDir(target)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if isRewrittenCacheFile(rel) {
			return copyNewFile(path, target)
		}
		return os.Symlink(path, target)
	})
}

func isRewrittenCacheFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "bundles/hashes/") || strings.HasSuffix(rel, ".prefetch")
}

func copyNewFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// ensureRealDir makes path a directory that is not a symlink, replacing
// anything else found there. A Git checkout can put a symlink anywhere in a
// project, including inside the build directory, and the server must never
// create files through one.
func ensureRealDir(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Mkdir(path, 0755)
}
