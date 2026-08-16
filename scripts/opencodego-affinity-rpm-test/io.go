package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	rootcommon "github.com/QuantumNous/new-api/common"
)

type durableJSONLWriter struct {
	file   *os.File
	buffer *bufio.Writer
}

var (
	authorizationPattern  = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s,;]+`)
	affinityHeaderPattern = regexp.MustCompile(`(?i)(x-newapi-affinity-key\s*:\s*)[^\s,;]+`)
	jsonSecretPattern     = regexp.MustCompile(`(?i)("(?:key|token|secret|api_key)"\s*:\s*")[^"]*(")`)
)

func newDurableJSONLWriter(path string) (*durableJSONLWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &durableJSONLWriter{file: file, buffer: bufio.NewWriterSize(file, 64*1024)}, nil
}

func (writer *durableJSONLWriter) Write(value any) error {
	data, err := rootcommon.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.buffer.Write(append(data, '\n'))
	return err
}

func (writer *durableJSONLWriter) Sync() error {
	if err := writer.buffer.Flush(); err != nil {
		return err
	}
	return writer.file.Sync()
}

func (writer *durableJSONLWriter) Close() error {
	flushErr := writer.Sync()
	closeErr := writer.file.Close()
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func ensureRunDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("run directory is required")
	}
	return os.MkdirAll(path, 0o700)
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		backup := path + ".replace"
		if backupData, backupErr := os.ReadFile(backup); backupErr == nil {
			data = backupData
			err = nil
			_ = os.Rename(backup, path)
		}
	}
	if err != nil {
		return err
	}
	return rootcommon.Unmarshal(data, target)
}

func writeJSONAtomic(path string, value any) error {
	data, err := rootcommon.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeBytesAtomic(path, data, 0o600)
}

func writeBytesAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err == nil {
		return nil
	}
	// Windows cannot atomically replace an existing file with os.Rename.
	backup := path + ".replace"
	_ = os.Remove(backup)
	if err := os.Rename(path, backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func appendJSONLine(path string, value any) error {
	data, err := rootcommon.Marshal(value)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hash8(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}

func redactSecret(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return "sha256:" + hash8(trimmed)
}

func sanitizePreview(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = authorizationPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = affinityHeaderPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = jsonSecretPattern.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	if len(value) > 300 {
		value = value[:300]
	}
	words := strings.Fields(value)
	for index, word := range words {
		if strings.HasPrefix(word, "sk-") && len(word) > 10 {
			words[index] = "[REDACTED_KEY]"
		}
	}
	return strings.Join(words, " ")
}

func readJSONLines[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make([]T, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var item T
		if err := rootcommon.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		result = append(result, item)
	}
	return result, scanner.Err()
}
