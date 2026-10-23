package mail

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	// Register MIME charsets (UTF-8, GBK, GB18030, ...).
	_ "github.com/emersion/go-message/charset"
)

// Parse decodes a raw RFC822 message and extracts the headers and text body.
func Parse(raw []byte, maxBody int64) (Message, error) {
	if maxBody <= 0 {
		maxBody = 256 << 10
	}

	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return Message{}, fmt.Errorf("parse message: %w", err)
	}

	var msg Message
	h := mr.Header
	if subject, err := h.Subject(); err == nil {
		msg.Subject = subject
	}
	if date, err := h.Date(); err == nil {
		msg.Date = date
	}
	if id, err := h.MessageID(); err == nil {
		msg.MessageID = id
	}
	if from, err := h.AddressList("From"); err == nil {
		msg.From = formatAddresses(from)
	}
	if to, err := h.AddressList("To"); err == nil {
		msg.To = formatAddresses(to)
	}
	if msg.Date.IsZero() {
		msg.Date = time.Now()
	}

	var plain, htmlText strings.Builder
	var truncated bool
	for {
		part, partErr := mr.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil && !message.IsUnknownCharset(partErr) {
			return msg, fmt.Errorf("read mime part: %w", partErr)
		}
		if part == nil {
			break
		}

		switch ph := part.Header.(type) {
		case *gomail.InlineHeader:
			switch inlineMediaType(ph) {
			case "text/html":
				text, cut, readErr := readPart(part.Body, maxBody)
				if readErr != nil {
					return msg, fmt.Errorf("read html part: %w", readErr)
				}
				htmlText.WriteString(text)
				truncated = truncated || cut
			case "text/plain":
				text, cut, readErr := readPart(part.Body, maxBody)
				if readErr != nil {
					return msg, fmt.Errorf("read text part: %w", readErr)
				}
				plain.WriteString(text)
				truncated = truncated || cut
			}
		case *gomail.AttachmentHeader:
			// Attachments are ignored on purpose.
		}
	}

	body := normalizeNewlines(strings.TrimSpace(plain.String()))
	if body == "" && htmlText.Len() > 0 {
		body = htmlToText(htmlText.String())
	}
	body, cut := truncateRunes(body, maxBody)
	msg.Body = body
	msg.BodyTruncated = truncated || cut
	return msg, nil
}

func inlineMediaType(h *gomail.InlineHeader) string {
	ct, _, err := h.ContentType()
	if err != nil || strings.TrimSpace(ct) == "" {
		return "text/plain"
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return strings.ToLower(ct)
	}
	return strings.ToLower(mediaType)
}

func formatAddresses(addrs []*gomail.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr == nil {
			continue
		}
		parts = append(parts, addr.String())
	}
	return strings.Join(parts, ", ")
}

func readPart(r io.Reader, limit int64) (string, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return "", false, err
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	return strings.ToValidUTF8(string(data), ""), truncated, nil
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func truncateRunes(s string, maxBytes int64) (string, bool) {
	if int64(len(s)) <= maxBytes {
		return s, false
	}
	cut := int(maxBytes)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

var (
	htmlScriptOrStyle = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>`)
	htmlComment       = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlBlockTag      = regexp.MustCompile(`(?i)</?(address|article|aside|blockquote|br|dd|div|dl|dt|fieldset|figcaption|figure|footer|form|h[1-6]|header|hr|li|main|nav|ol|p|pre|section|table|tbody|td|tfoot|th|thead|tr|ul)\b[^>]*>`)
	htmlAnyTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	htmlManyNewlines  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText converts an HTML email body to readable plain text.
func htmlToText(s string) string {
	s = htmlScriptOrStyle.ReplaceAllString(s, " ")
	s = htmlComment.ReplaceAllString(s, " ")
	s = htmlBlockTag.ReplaceAllString(s, "\n")
	s = htmlAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = normalizeNewlines(s)

	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	s = strings.Join(lines, "\n")
	s = htmlManyNewlines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
