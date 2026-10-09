package notify

import (
	"testing"

	"opencloud-backup-plugin/internal/config"
)

func TestSMTPEnv_Config(t *testing.T) {
	var e SMTPEnv
	err := config.Load([]string{
		"SMTP_HOST=smtp.example", "SMTP_PORT=587", "SMTP_USERNAME=u", "SMTP_PASSWORD=p",
		"SMTP_FROM=from@example", "NOTIFY_OPERATOR_EMAIL=ops@example",
	}, &e)
	if err != nil {
		t.Fatal(err)
	}
	want := SMTPConfig{
		Host: "smtp.example", Port: 587, Username: "u", Password: "p",
		From: "from@example", OperatorTo: "ops@example",
	}
	if got := e.Config(); got != want || !got.Valid() {
		t.Fatalf("Config = %+v", got)
	}
}

func TestSMTPEnv_UnsetIsNoMail(t *testing.T) {
	var e SMTPEnv
	if err := config.Load(nil, &e); err != nil {
		t.Fatal(err)
	}
	if e.Config().Valid() {
		t.Fatal("an unset environment configures mail")
	}
}
