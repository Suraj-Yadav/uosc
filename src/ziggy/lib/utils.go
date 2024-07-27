package lib

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type ErrorData struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

func Check(err error) {
	if err != nil {
		res := ErrorData{Error: true, Message: err.Error()}
		json, err := json.Marshal(res)
		if err != nil {
			panic(err)
		}
		fmt.Print(string(json))
		os.Exit(0)
	}
}

func Must[T any](t T, err error) T {
	Check(err)
	return t
}

const OSDBChunkSize = 65536 // 64k

type chunkInfo struct {
	offset int64
	size   int64
}

func readRemoteChunks(url string, minimumRequiredSize int64, chunks ...chunkInfo) (fileSize int64, buf []byte, err error) {
	client := &http.Client{}

	ctx, cancelFunc := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFunc()

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return
	}

	res, err := client.Do(req)
	if err != nil {
		return
	}

	header := res.Header
	if accept_ranges, ok := header["Accept-Ranges"]; !ok || accept_ranges[0] != "bytes" {
		err = errors.New("URL doesn't support range fetch")
		return
	}

	fileSize, err = strconv.ParseInt(header["Content-Length"][0], 10, 64)
	if err != nil {
		return
	}

	if fileSize < minimumRequiredSize {
		err = errors.New("file is too small to generate a valid hash")
		return
	}

	totalBufferNeeded := int64(0)
	for _, span := range chunks {
		totalBufferNeeded += span.size
	}

	buf = make([]byte, totalBufferNeeded)
	filled := 0
	for _, span := range chunks {
		start := span.offset
		if start < 0 {
			start += fileSize
		}
		err = readRemoteChunk(ctx, client, url, start, buf[filled:filled+int(span.size)])
		if err != nil {
			return
		}
		filled += int(span.size)
	}
	return fileSize, buf, nil
}

func readChunks(filePath string, minimumRequiredSize int64, chunks ...chunkInfo) (fileSize int64, buf []byte, err error) {
	if strings.HasPrefix(filePath, "http://") || strings.HasPrefix(filePath, "https://") {
		fileSize, buf, err = readRemoteChunks(filePath, OSDBChunkSize, chunks...)
		return
	}

	file, err := os.Open(filePath)
	if err != nil {
		err = errors.New("couldn't open file for hashing")
		return
	}

	fi, err := file.Stat()
	if err != nil {
		err = errors.New("couldn't stat file for hashing")
		return
	}

	fileSize = fi.Size()
	if fileSize < minimumRequiredSize {
		err = errors.New("file is too small to generate a valid hash")
		return
	}

	totalBufferNeeded := int64(0)
	for _, span := range chunks {
		totalBufferNeeded += span.size
	}

	buf = make([]byte, totalBufferNeeded)
	filled := 0
	for _, span := range chunks {
		start := span.offset
		if start < 0 {
			start += fileSize
		}
		err = readChunk(file, start, buf[filled:filled+int(span.size)])
		if err != nil {
			return
		}
		filled += int(span.size)
	}

	return fileSize, buf, nil
}

// Generate an OSDB hash for a file.
func OSDBHashFile(filePath string) (hash string, err error) {
	var buf []byte
	fileSize := int64(0)

	spans := []chunkInfo{
		{0, OSDBChunkSize},
		{-OSDBChunkSize, OSDBChunkSize},
	}

	fileSize, buf, err = readChunks(filePath, OSDBChunkSize, spans...)

	if err != nil {
		return "", err
	}

	// Convert to uint64, and sum
	var nums [(OSDBChunkSize * 2) / 8]uint64
	reader := bytes.NewReader(buf)
	err = binary.Read(reader, binary.LittleEndian, &nums)
	if err != nil {
		return "", err
	}
	var hashUint uint64
	for _, num := range nums {
		hashUint += num
	}

	hashUint = hashUint + uint64(fileSize)

	return fmt.Sprintf("%016x", hashUint), nil
}

func readRemoteChunk(ctx context.Context, client *http.Client, url string, offset int64, buf []byte) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	range_header := "bytes=" + strconv.Itoa(int(offset)) + "-" + strconv.Itoa(int(offset)+len(buf)-1)
	req.Header.Add("Range", range_header)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	n, err := io.ReadFull(resp.Body, buf)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return fmt.Errorf("invalid read %v", n)
	}
	return nil
}

// Read a chunk of a file at `offset` so as to fill `buf`.
func readChunk(file *os.File, offset int64, buf []byte) (err error) {
	n, err := file.ReadAt(buf, offset)
	if err != nil {
		return err
	}
	if n != OSDBChunkSize {
		return fmt.Errorf("invalid read %v", n)
	}
	return
}

// Because the default `json.Marshal` HTML escapes `&,<,>` characters and it can't be turned off...
func JSONMarshal(t interface{}) ([]byte, error) {
	buffer := &bytes.Buffer{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(t)
	return buffer.Bytes(), err
}
