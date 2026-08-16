package kiya

import (
	"fmt"
	"net/smtp"
	"os"
)

// SendMail mengirim email menggunakan SMTP (HTML text didukung).
// Fungsi ini membutuhkan environment variable berikut:
// - SMTP_HOST (contoh: smtp.gmail.com)
// - SMTP_PORT (contoh: 587)
// - SMTP_USER (alamat email pengirim)
// - SMTP_PASS (app password)
// - SMTP_FROM_NAME (nama pengirim)
func SendMail(to []string, subject string, htmlBody string) error {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	fromName := os.Getenv("SMTP_FROM_NAME")

	if host == "" || port == "" || user == "" || pass == "" {
		return fmt.Errorf("smtp configuration is missing in environment variables")
	}

	auth := smtp.PlainAuth("", user, pass, host)

	if len(to) == 0 {
		return fmt.Errorf("recipient list is empty")
	}

	from := fmt.Sprintf("\"%s\" <%s>", fromName, user)
	msg := []byte("From: " + from + "\r\n" +
		"To: " + to[0] + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-version: 1.0;\r\n" +
		"Content-Type: text/html; charset=\"UTF-8\";\r\n\r\n" +
		htmlBody + "\r\n")

	addr := fmt.Sprintf("%s:%s", host, port)
	err := smtp.SendMail(addr, auth, user, to, msg)
	if err != nil {
		LogError("[Mailer] Failed to send email to %s: %v", to[0], err)
		return err
	}
	return nil
}
