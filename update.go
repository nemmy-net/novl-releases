package main

import (
	"compress/gzip"
	sha256 "crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
)

type Version struct {
	major int
	minor int
	patch int
}

type VersionMap struct {
	// The latest version for every major number. e.g. "3" -> "3.2.49"
	Latest map[string]string
	// Every version and its files
	Releases map[string]FileMap
}

type PlatformMap map[string]VersionMap

// Map filenames to their ETag (SHA256 hash). Each key is a file path starting from the root of the repo.
type FileMap map[string]string

func main() {
	platforms, err := os.ReadDir(".")
	errorCheck(err, "Failed to read releases directory")

	if err := os.Mkdir("gzip", os.ModeDir); err != nil && !errors.Is(err, os.ErrExist) {
		log.Fatalf("Failed to create gzip folder: %v", err)
	}

	// Record all etags so we can find leftover gzip files
	etags := make(map[string]bool)
	platformMap := make(PlatformMap)

	for _, platformDir := range platforms {
		if !platformDir.IsDir() || platformDir.Name() == "gzip" || platformDir.Name() == ".git" {
			continue
		}
		entries, err := os.ReadDir(platformDir.Name())
		errorCheck(err, "Failed to read dir: %v", platformDir.Name())

		var versions []Version // Stores parsed versions so we can find the latest
		versionMap := VersionMap{
			Latest:   make(map[string]string),
			Releases: make(map[string]FileMap),
		}
		platformMap[platformDir.Name()] = versionMap

		for _, entry := range entries {
			parts := strings.Split(entry.Name(), ".")
			if len(parts) != 3 {
				log.Fatalf("Invalid folder %v: Name must be x.y.z format", entry.Name())
			}

			parsedParts := [3]int64{}
			for i := range parts {
				parsedParts[i], err = strconv.ParseInt(parts[i], 10, 32)
				errorCheck(err, "Invalid folder %v: Name must be x.y.z format", entry.Name())
			}
			versions = append(versions, Version{
				major: int(parsedParts[0]),
				minor: int(parsedParts[1]),
				patch: int(parsedParts[2]),
			})
		}

		slices.SortFunc(versions, func(a Version, b Version) int {
			if a.major-b.major != 0 {
				return a.major - b.major
			} else if a.minor-b.minor != 0 {
				return a.minor - b.minor
			}
			return a.patch - b.patch
		})

		for i, version := range versions {
			versionStr := fmt.Sprintf("%v.%v.%v", version.major, version.minor, version.patch)
			fileMap := make(FileMap)

			entries, err := os.ReadDir(fmt.Sprint(platformDir.Name(), "/", versionStr))
			if err != nil {
				log.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				filePath := fmt.Sprint(platformDir.Name(), "/", versionStr, "/", entry.Name())
				etag, err := processFile(filePath)
				errorCheck(err, "Failed to hash file")
				etags[etag] = true
				fileMap[filePath] = etag
			}

			versionMap.Releases[versionStr] = fileMap
			if i == len(versions)-1 || versions[i+1].major != version.major {
				versionMap.Latest[fmt.Sprint(version.major)] = versionStr
			}
		}
	}

	// Purge files from gzip that no longer exist
	gzipEntries, err := os.ReadDir("gzip")
	if err != nil {
		log.Fatal(err)
	}
	erasedEntries := 0
	for _, entry := range gzipEntries {
		if !etags[entry.Name()] {
			err := os.Remove("gzip/" + entry.Name())
			errorCheck(err, "")
			erasedEntries += 1
		}
	}
	if erasedEntries > 0 {
		log.Printf("Erased %v unused gzip entries", erasedEntries)
	}

	indexJson, err := json.Marshal(platformMap)
	errorCheck(err, "")
	file, err := os.Create("index.json")
	errorCheck(err, "Failed to create index.json")
	defer file.Close()
	if _, err := file.Write(indexJson); err != nil {
		log.Fatal(err)
	}
}

// Compute file hash and generate a gzipped version in the gzip folder
func processFile(path string) (etag string, err error) {
	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()

	hasher := sha256.New()
	buffer := make([]byte, 4096)
	for {
		read, err := source.Read(buffer)
		if err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
		wrote, err := hasher.Write(buffer[:read])
		if err != nil {
			return "", err
		} else if wrote != read {
			return "", fmt.Errorf("%v bytes were to be hashed but only %v were accepted", read, wrote)
		}
	}

	etag = hex.EncodeToString(hasher.Sum(nil))

	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	gzPath := "gzip/" + etag
	_, err = os.Stat(gzPath)
	if err == nil {
		return etag, nil // Gzipped file already exists
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	log.Printf("Creating gzip of: %v", path)

	// Gzip to temp file incase anything goes wrong
	tempGzPath := gzPath + "~"
	gzipped, err := os.Create(tempGzPath)
	if err != nil {
		return "", err
	}

	defer gzipped.Close()
	zipper, err := gzip.NewWriterLevel(gzipped, gzip.BestCompression)
	if err != nil {
		return "", err
	}

	defer zipper.Close()
	for {
		read, err := source.Read(buffer)
		if err == io.EOF {
			if err := zipper.Flush(); err != nil {
				return "", err
			}
			break
		} else if err != nil {
			return "", err
		}
		wrote, err := zipper.Write(buffer[:read])
		if err != nil {
			return "", err
		} else if wrote != read {
			return "", fmt.Errorf("%v bytes were to be compressed but only %v were accepted", read, wrote)
		}
	}

	zipper.Close()
	gzipped.Close()

	if err := os.Rename(tempGzPath, gzPath); err != nil {
		return "", err
	}
	return etag, nil
}

func errorCheck(err error, fmt string, args ...any) {
	if err == nil {
		return
	}
	log.Printf(fmt, args...)
	panic(err)
}
