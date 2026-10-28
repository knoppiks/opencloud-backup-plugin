# Your own CA

Put the PEM certificate of your own CA here (any file name), when
OpenCloud's certificate or a backup destination's comes from one. The backup
service trusts it in addition to the public CAs. Leave the directory as it is
with Let's Encrypt.

Do not set `INSECURE` for this: the backup service never turns certificate
checks off.
