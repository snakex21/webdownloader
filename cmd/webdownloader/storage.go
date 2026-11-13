package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

const applicationDirectoryName = "WebDownloader"

type storagePaths struct {
	dataDir   string
	prefsFile string
	logFile   string
	outputDir string
	portable  bool
}

var appStorage storagePaths

func configureStorage(portable bool) error {
	executableDir, err := executableDirectory()
	if err != nil {
		return err
	}

	paths := storagePaths{portable: portable}
	if portable {
		paths.dataDir = executableDir
		paths.outputDir = filepath.Join(executableDir, "output")
	} else {
		localDataDir, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("locate user data directory: %w", err)
		}
		paths.dataDir = filepath.Join(localDataDir, applicationDirectoryName)

		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate user home directory: %w", err)
		}
		paths.outputDir = filepath.Join(homeDir, "Downloads", applicationDirectoryName)
	}

	paths.prefsFile = filepath.Join(paths.dataDir, "prefs.json")
	paths.logFile = filepath.Join(paths.dataDir, "webdownloader.log")

	if err := os.MkdirAll(paths.dataDir, 0o755); err != nil {
		return fmt.Errorf("create user data directory: %w", err)
	}

	appStorage = paths
	if !portable {
		migratePortablePreferences(filepath.Join(executableDir, "prefs.json"), paths.prefsFile)
	}
	return nil
}

func executableDirectory() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		workingDir, workingDirErr := os.Getwd()
		if workingDirErr != nil {
			return "", fmt.Errorf("locate executable directory: %w", err)
		}
		return workingDir, nil
	}
	return filepath.Dir(executable), nil
}

func migratePortablePreferences(source, destination string) {
	if _, err := os.Stat(destination); err == nil {
		return
	}

	data, err := os.ReadFile(source)
	if err != nil {
		return
	}
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		log.Printf("migrate preferences: %v", err)
	}
}

func setupLogging() (io.Closer, error) {
	if appStorage.logFile == "" {
		return nil, fmt.Errorf("storage is not configured")
	}

	file, err := os.OpenFile(appStorage.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	log.SetOutput(file)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("starting %s (portable=%t)", applicationTitle, appStorage.portable)
	return file, nil
}

func defaultOutputBase() string {
	if appStorage.outputDir != "" {
		return appStorage.outputDir
	}

	executableDir, err := executableDirectory()
	if err != nil {
		return "output"
	}
	return filepath.Join(executableDir, "output")
}
