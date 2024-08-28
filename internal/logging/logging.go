package logging

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// SetupLogger sets up logging to a file with a timestamped filename in the "logs" directory.
func SetupLogger() func() {
	logDir := "logs"

	// Attempt to create the log directory
	err := os.Mkdir(logDir, 0755)
	if err != nil {
		if os.IsPermission(err) {
			fmt.Println("Failed to create log directory due to insufficient permissions. Trying with sudo...")

			// Retry with sudo
			err = sudoMakeDir(logDir)
			if err != nil {
				log.Fatalf("Failed to create log directory even with sudo: %v", err)
			}
		} else if !os.IsExist(err) {
			log.Fatalf("Failed to create log directory: %v", err)
		}
	}

	// Create a new log file with date and time
	logFileName := fmt.Sprintf("seho_%s.log", time.Now().Format("20060102_150405"))
	logFilePath := filepath.Join(logDir, logFileName)
	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}

	// Set log output to file
	log.SetOutput(logFile)

	// Return a cleanup function to close the log file
	return func() {
		logFile.Close()
	}
}

// sudoMakeDir attempts to create a directory using sudo
func sudoMakeDir(dir string) error {
	cmd := exec.Command("sudo", "mkdir", "-p", dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

