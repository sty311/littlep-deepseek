package bridge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"
)

const chatPath = "/teacherp/chat/ask/sse"

// These are local safety limits, not claimed limits of Little P or DeepSeek.
const maxImageBytes = 32 << 20
const maxRequestBytes = 33 << 20
const maxFieldBytes = 64 << 10
const defaultScanQuestion = "请解答图片中的题目。"

// The request log names messageImages as an argument, but does not expose the
// multipart part headers. Keep the unverified file-part mapping in one place.
const scanFilePartName = "messageImages"

type bridge struct {
	sessions *SessionStore
	client   deepSeekClient
	busy     chan struct{}
	search   SearchProvider
}

type Input struct {
	Scene    string
	ChatID   string
	Question string
	Scan     *ScanInput
}

type ScanInput struct {
	Bytes         []byte
	MIME          string
	SHA256        [32]byte
	Width         int
	Height        int
	ReceivedUTC   time.Time
	ParseDuration time.Duration
}

type chatRequest = Input

type inputError struct {
	status  int
	message string
}

func (e inputError) Error() string { return e.message }

func inputStatus(err error) int {
	var typed inputError
	if errors.As(err, &typed) {
		return typed.status
	}
	return http.StatusBadRequest
}

func parseChatRequest(w http.ResponseWriter, r *http.Request) (chatRequest, error) {
	received := time.Now().UTC()
	if r.Method != http.MethodPost {
		return chatRequest{}, errors.New("POST required")
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		return chatRequest{}, errors.New("multipart/form-data required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return chatRequest{}, errors.New("invalid multipart request")
	}
	fields := make(map[string]string)
	var imageBytes []byte
	seenFile := false
	partsSeen := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return chatRequest{}, inputError{http.StatusRequestEntityTooLarge, "请求超过大小限制"}
			}
			return chatRequest{}, errors.New("invalid multipart request")
		}
		partsSeen++
		if partsSeen > 64 {
			return chatRequest{}, inputError{http.StatusRequestEntityTooLarge, "请求字段过多"}
		}
		name := part.FormName()
		if name == "" {
			return chatRequest{}, errors.New("unnamed multipart part")
		}
		if part.FileName() != "" {
			_, fieldExists := fields[name]
			if name != scanFilePartName || seenFile || fieldExists {
				return chatRequest{}, errors.New("图片文件重复或字段不符")
			}
			seenFile = true
			// Filename is metadata only; never use it as a filesystem path.
			_, disposition, dispositionErr := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			if dispositionErr != nil || len(disposition["filename"]) > 255 {
				return chatRequest{}, errors.New("图片文件名无效")
			}
			imageBytes, err = io.ReadAll(io.LimitReader(part, maxImageBytes+1))
			if err != nil || len(imageBytes) > maxImageBytes {
				return chatRequest{}, inputError{http.StatusRequestEntityTooLarge, "图片超过大小限制"}
			}
			if len(imageBytes) == 0 {
				return chatRequest{}, errors.New("图片为空或超过大小限制")
			}
			continue
		}
		coreField := name == "messageScene" || name == "chatId" || name == "messageContents" || name == scanFilePartName
		if !coreField {
			// Unrelated native metadata may be present, but its value is never
			// needed and must not accumulate in memory.
			n, err := io.Copy(io.Discard, io.LimitReader(part, maxFieldBytes+1))
			if err != nil {
				return chatRequest{}, errors.New("invalid multipart request")
			}
			if n > maxFieldBytes {
				return chatRequest{}, inputError{http.StatusRequestEntityTooLarge, "请求字段超过大小限制"}
			}
			continue
		}
		if _, exists := fields[name]; exists || (name == scanFilePartName && seenFile) {
			return chatRequest{}, fmt.Errorf("multiple %s fields", name)
		}
		data, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
		if err != nil || len(data) > maxFieldBytes {
			return chatRequest{}, inputError{http.StatusRequestEntityTooLarge, "请求字段超过大小限制"}
		}
		fields[name] = string(data)
	}
	input, err := normalizeIncomingRequest(fields, imageBytes)
	if err == nil && input.Scan != nil {
		input.Scan.ReceivedUTC = received
		input.Scan.ParseDuration = time.Since(received)
		log.Printf("[LITTLEP-BRIDGE] scan_input received_utc=%s parse_ms=%d bytes=%d width=%d height=%d mime=%s sha256=%x", received.Format(time.RFC3339Nano), input.Scan.ParseDuration.Milliseconds(), len(input.Scan.Bytes), input.Scan.Width, input.Scan.Height, input.Scan.MIME, input.Scan.SHA256)
	}
	return input, err
}

func normalizeIncomingRequest(fields map[string]string, imageBytes []byte) (Input, error) {
	scene, chatID, contents := fields["messageScene"], fields["chatId"], fields["messageContents"]
	if scene == "" || len(scene) > 128 || len(chatID) > 128 || contents == "" || len(contents) > 4096 {
		return Input{}, errors.New("missing or oversized chat fields")
	}
	var items []struct {
		Type string `json:"type"`
		Text struct {
			Content string `json:"content"`
		} `json:"text"`
		Image struct {
			Index  string `json:"idx"`
			Format string `json:"format"`
		} `json:"image"`
	}
	if err := json.Unmarshal([]byte(contents), &items); err != nil {
		return Input{}, errors.New("invalid messageContents JSON")
	}
	if len(items) == 0 || len(items) > 2 {
		return Input{}, errors.New("unsupported messageContents items")
	}
	var question string
	textCount, imageCount := 0, 0
	for _, item := range items {
		switch item.Type {
		case "text":
			textCount++
			question = item.Text.Content
			if textCount > 1 || strings.TrimSpace(question) == "" || len(question) > 4096 {
				return Input{}, errors.New("invalid text message")
			}
		case "image":
			imageCount++
			if imageCount > 1 || item.Image.Index != "0" || (item.Image.Format != "jpg" && item.Image.Format != "jpeg") {
				return Input{}, errors.New("图片描述无效")
			}
		default:
			return Input{}, errors.New("unsupported messageContents item")
		}
	}
	if imageCount == 0 {
		_, imageFieldExists := fields[scanFilePartName]
		if len(imageBytes) != 0 || imageFieldExists {
			return Input{}, errors.New("图片输入缺少图片描述")
		}
		if textCount != 1 {
			return Input{}, errors.New("only one text message is currently supported")
		}
		return Input{Scene: scene, ChatID: chatID, Question: question}, nil
	}
	_, imageFieldExists := fields[scanFilePartName]
	if scene != "dayiPracticeScan" || len(imageBytes) == 0 || imageFieldExists {
		return Input{}, errors.New("扫描输入须上传 JPEG 文件")
	}
	if http.DetectContentType(imageBytes) != "image/jpeg" {
		return Input{}, errors.New("图片内容须为 JPEG")
	}
	// A bounded header parse plus SOI/EOI markers catches common truncated
	// uploads without allocating an 8192x8192 decoded bitmap on the device.
	if !bytes.HasPrefix(imageBytes, []byte{0xff, 0xd8}) || bytes.LastIndex(imageBytes, []byte{0xff, 0xd9}) < 2 {
		return Input{}, errors.New("JPEG 数据不完整")
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(imageBytes))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 {
		return Input{}, errors.New("JPEG 尺寸无效或超限")
	}
	if textCount == 0 {
		question = defaultScanQuestion
	}
	return Input{Scene: scene, ChatID: chatID, Question: question, Scan: &ScanInput{
		Bytes: imageBytes, MIME: "image/jpeg", SHA256: sha256.Sum256(imageBytes), Width: config.Width, Height: config.Height,
	}}, nil
}

func randomID(prefix string) (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return prefix + strings.ToUpper(hex.EncodeToString(bytes[:])), nil
}
