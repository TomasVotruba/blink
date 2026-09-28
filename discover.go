package main

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const defaultSuffix = "Test.php"

type phpunitConfig struct {
	TestSuites []struct {
		Directories []struct {
			Path   string `xml:",chardata"`
			Suffix string `xml:"suffix,attr"`
		} `xml:"directory"`
		Files    []string `xml:"file"`
		Excludes []string `xml:"exclude"`
	} `xml:"testsuites>testsuite"`
}

// findConfig returns the given config path, or phpunit.xml / phpunit.xml.dist from the current directory.
func findConfig(configPath string) (string, error) {
	if configPath != "" {
		if _, err := os.Stat(configPath); err != nil {
			return "", err
		}

		return configPath, nil
	}

	for _, name := range []string{"phpunit.xml", "phpunit.xml.dist"} {
		if _, err := os.Stat(name); err == nil {
			return name, nil
		}
	}

	return "", nil
}

// discoverFromConfig collects test files from all <testsuite> elements of a PHPUnit config.
func discoverFromConfig(configPath string) ([]string, error) {
	content, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config phpunitConfig
	if err := xml.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}

	baseDir := filepath.Dir(configPath)
	resolve := func(path string) string {
		return filepath.Join(baseDir, strings.TrimSpace(path))
	}

	var files []string
	for _, suite := range config.TestSuites {
		var excludes []string
		for _, exclude := range suite.Excludes {
			excludes = append(excludes, resolve(exclude))
		}

		for _, directory := range suite.Directories {
			suffix := directory.Suffix
			if suffix == "" {
				suffix = defaultSuffix
			}

			dirs, err := filepath.Glob(resolve(directory.Path))
			if err != nil {
				return nil, err
			}

			for _, dir := range dirs {
				found, err := walkTestFiles(dir, suffix, excludes)
				if err != nil {
					return nil, err
				}
				files = append(files, found...)
			}
		}

		for _, file := range suite.Files {
			files = append(files, resolve(file))
		}
	}

	return uniqueSorted(files), nil
}

// discoverFromPaths collects test files from paths given on the command line.
func discoverFromPaths(paths []string) ([]string, error) {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}

		if !info.IsDir() {
			files = append(files, filepath.Clean(path))
			continue
		}

		found, err := walkTestFiles(path, defaultSuffix, nil)
		if err != nil {
			return nil, err
		}
		files = append(files, found...)
	}

	return uniqueSorted(files), nil
}

func walkTestFiles(dir, suffix string, excludes []string) ([]string, error) {
	var files []string

	// trailing separator makes WalkDir follow a symlinked root directory
	err := filepath.WalkDir(dir+string(filepath.Separator), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if slices.Contains(excludes, path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !entry.IsDir() && strings.HasSuffix(path, suffix) {
			files = append(files, path)
		}

		return nil
	})

	return files, err
}

func uniqueSorted(files []string) []string {
	slices.Sort(files)
	return slices.Compact(files)
}
