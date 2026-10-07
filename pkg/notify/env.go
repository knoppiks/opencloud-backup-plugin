package notify

import "opencloud-backup-plugin/internal/config"

// SMTPEnv is the mail server operator notifications go through, as the
// environment declares it (internal/config). All of host, port, sender and
// recipient are needed; otherwise notifications are recorded and logged only.
type SMTPEnv struct {
	Host       string        `env:"SMTP_HOST" doc:"Mail server for operator notifications."`
	Port       int           `env:"SMTP_PORT" doc:"Mail server port, such as 587. 0 disables mail."`
	Username   string        `env:"SMTP_USERNAME" doc:"Mail server login, if it requires one."`
	Password   config.Secret `env:"SMTP_PASSWORD" doc:"Mail server password."`
	From       string        `env:"SMTP_FROM" doc:"Sender address."`
	OperatorTo string        `env:"NOTIFY_OPERATOR_EMAIL" doc:"Where operator notifications go. They never name a Space."`
}

// Config is the SMTP sink's configuration from e.
func (e SMTPEnv) Config() SMTPConfig {
	return SMTPConfig{
		Host:       e.Host,
		Port:       e.Port,
		Username:   e.Username,
		Password:   e.Password.Reveal(),
		From:       e.From,
		OperatorTo: e.OperatorTo,
	}
}
