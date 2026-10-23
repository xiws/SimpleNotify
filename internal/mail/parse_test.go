package mail

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const plainMessage = "From: Alice <alice@example.com>\r\n" +
	"To: Bob <bob@example.com>\r\n" +
	"Subject: =?UTF-8?B?" + "5L2g5aW977yM5LiW55WM" + "?=\r\n" +
	"Date: Tue, 2 Jan 2024 10:00:00 +0800\r\n" +
	"Message-ID: <msg1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"hello world\r\n"

func TestParsePlain(t *testing.T) {
	msg, err := Parse([]byte(plainMessage), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Subject != "你好，世界" {
		t.Errorf("subject = %q", msg.Subject)
	}
	if msg.From != "\"Alice\" <alice@example.com>" {
		t.Errorf("from = %q", msg.From)
	}
	if msg.To != "\"Bob\" <bob@example.com>" {
		t.Errorf("to = %q", msg.To)
	}
	if msg.MessageID != "msg1@example.com" {
		t.Errorf("message id = %q", msg.MessageID)
	}
	if msg.Date.IsZero() || msg.Date.Year() != 2024 {
		t.Errorf("date = %v", msg.Date)
	}
	if msg.Body != "hello world" {
		t.Errorf("body = %q", msg.Body)
	}
	if msg.BodyTruncated {
		t.Error("body should not be truncated")
	}
}

func TestParseMultipartAlternativePrefersPlain(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: multi\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain text\r\n" +
		"--b1\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html text</p>\r\n" +
		"--b1--\r\n"
	msg, err := Parse([]byte(raw), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Body != "plain text" {
		t.Fatalf("body = %q", msg.Body)
	}
}

func TestParseHTMLOnly(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: html\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>p{color:red}</style><script>alert(1)</script></head>" +
		"<body><p>你好 &amp; 世界</p><p>第二行</p></body></html>\r\n"
	msg, err := Parse([]byte(raw), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Contains(msg.Body, "color:red") || strings.Contains(msg.Body, "alert") {
		t.Errorf("script/style leaked into body: %q", msg.Body)
	}
	if !strings.Contains(msg.Body, "你好 & 世界") || !strings.Contains(msg.Body, "第二行") {
		t.Errorf("body = %q", msg.Body)
	}
}

func TestParseGBKBody(t *testing.T) {
	body, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("中文正文内容"))
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: a@example.com\r\nSubject: gbk\r\n" +
		"Content-Type: text/plain; charset=gbk\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + string(body) + "\r\n"
	msg, err := Parse([]byte(raw), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Body != "中文正文内容" {
		t.Fatalf("body = %q", msg.Body)
	}
}

func TestParseIgnoresAttachments(t *testing.T) {
	attachment := base64.StdEncoding.EncodeToString([]byte("SECRET-ATTACHMENT"))
	raw := "From: a@example.com\r\nSubject: att\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: text/plain\r\n\r\nmain body\r\n" +
		"--b1\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"x.bin\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n\r\n" + attachment + "\r\n" +
		"--b1--\r\n"
	msg, err := Parse([]byte(raw), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Body != "main body" {
		t.Fatalf("body = %q", msg.Body)
	}
	if strings.Contains(msg.Body, "SECRET") || strings.Contains(msg.Body, "U0VDUkVU") {
		t.Fatalf("attachment leaked: %q", msg.Body)
	}
}

func TestParseBodyTruncation(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: t\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n你好世界"
	msg, err := Parse([]byte(raw), 4)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Body != "你" {
		t.Fatalf("body = %q", msg.Body)
	}
	if !msg.BodyTruncated {
		t.Fatal("expected BodyTruncated")
	}
}

func TestParseNoTextParts(t *testing.T) {
	raw := "From: a@example.com\r\nSubject: empty\r\n\r\n"
	msg, err := Parse([]byte(raw), 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if msg.Body != "" {
		t.Fatalf("body = %q", msg.Body)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText("<div>a<br>b</div><p>c &lt; d</p>")
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") || !strings.Contains(got, "c < d") {
		t.Fatalf("got %q", got)
	}
}
