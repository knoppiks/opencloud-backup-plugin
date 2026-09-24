// Translations shipped by the extension.
//
// `ClassicApplicationScript.translations` takes a `{lang: {msgid: string}}` map
// which the host merges into its own gettext instance, so the extension carries
// its own catalogue rather than depending on the OpenCloud release having one.
// Until 8c wired this, every `$gettext` call fell through to its msgid — which
// looked correct in English and was simply untranslated in German.
//
// English is the msgid and therefore needs no entry.
//
// **No extraction pipeline, deliberately (8d decision 8).** For two languages a
// `.pot`/`.po` toolchain is more moving parts than it is worth.
// `translations.spec.ts` keeps the catalogue honest instead. It fails when a
// msgid in the source has no German, when a German entry no longer matches any
// msgid, and when a German entry just copies its msgid or drops one of its
// `%{placeholders}`. Revisit if a third language arrives.

/** Translations maps a language tag to its catalogue. */
export type Translations = Record<string, Record<string, string>>

const de: Record<string, string> = {
  'Backup Vault': 'Backup-Tresor',
  Loading: 'Wird geladen',
  'Loading…': 'Wird geladen…',
  'Try again': 'Erneut versuchen',
  'There is nothing here yet.': 'Hier ist noch nichts.',
  'You do not have any spaces to back up.': 'Sie haben keine Spaces, die gesichert werden können.',
  'No backup destination has been shared with you yet. Ask your administrator to grant you one.':
    'Es wurde noch kein Sicherungsziel für Sie freigegeben. Bitten Sie Ihre Administration darum.',

  // Failure states (components/RequestState.vue).
  'The backup service cannot be reached': 'Der Sicherungsdienst ist nicht erreichbar',
  'The backup service did not answer in time':
    'Der Sicherungsdienst hat nicht rechtzeitig geantwortet',
  'Your session has expired': 'Ihre Sitzung ist abgelaufen',
  'You do not have access to this': 'Sie haben darauf keinen Zugriff',
  'This is not available': 'Das ist nicht verfügbar',
  'Backups are not set up for this space yet':
    'Für diesen Space ist noch keine Sicherung eingerichtet',
  'A backup is already running for this space': 'Für diesen Space läuft bereits eine Sicherung',
  'The backup destination cannot be used right now':
    'Das Sicherungsziel kann derzeit nicht verwendet werden',
  'The backup service is temporarily unavailable':
    'Der Sicherungsdienst ist vorübergehend nicht verfügbar',
  'OpenCloud did not answer the backup service':
    'OpenCloud hat dem Sicherungsdienst nicht geantwortet',
  'The backup service sent something unexpected':
    'Der Sicherungsdienst hat etwas Unerwartetes gesendet',
  'Something went wrong': 'Etwas ist schiefgelaufen',
  'Your backups are unaffected. Try again in a moment.':
    'Ihre Sicherungen sind davon nicht betroffen. Versuchen Sie es in einem Moment erneut.',
  'Reload the page to sign in again.': 'Laden Sie die Seite neu, um sich erneut anzumelden.',
  'Ask a manager of this space if you need access.':
    'Fragen Sie eine verwaltende Person dieses Spaces, wenn Sie Zugriff benötigen.',
  'This usually clears up on its own. Try again in a moment.':
    'Das erledigt sich meist von selbst. Versuchen Sie es in einem Moment erneut.',

  // Dates (format/format.ts).
  'Today, %{time}': 'Heute, %{time}',
  'Yesterday, %{time}': 'Gestern, %{time}',
  'Tomorrow, %{time}': 'Morgen, %{time}',

  // Space states (status/statetext.ts).
  'Not backed up': 'Nicht gesichert',
  'Setup not finished': 'Einrichtung nicht abgeschlossen',
  Running: 'Läuft',
  'Last backup failed': 'Letzte Sicherung fehlgeschlagen',
  'Backups have stopped': 'Sicherungen laufen nicht mehr',
  'Scheduled backups off': 'Geplante Sicherungen aus',
  'Waiting for first backup': 'Wartet auf die erste Sicherung',
  Protected: 'Geschützt',
  'Nothing in this space is being backed up.': 'Nichts in diesem Space wird gesichert.',
  'Backup setup was started but not finished, so nothing is backed up yet.':
    'Die Einrichtung der Sicherung wurde begonnen, aber nicht abgeschlossen. Es wird noch nichts gesichert.',
  'Earlier backups are still safe. The next scheduled run will try again.':
    'Frühere Sicherungen sind weiterhin sicher. Der nächste geplante Lauf versucht es erneut.',
  'Backups have not succeeded for longer than the schedule allows.':
    'Seit längerer Zeit, als der Zeitplan vorsieht, war keine Sicherung erfolgreich.',
  'Existing backups are kept, but no new ones are made unless started by hand.':
    'Vorhandene Sicherungen bleiben erhalten, neue entstehen aber nur, wenn Sie sie von Hand starten.',
  'The first backup will run at the next scheduled time.':
    'Die erste Sicherung läuft zum nächsten geplanten Zeitpunkt.',

  // Overview cards (components/SpaceCard.vue).
  'Personal space': 'Persönlicher Space',
  'Shared space': 'Geteilter Space',
  'Running since %{when}': 'Läuft seit %{when}',
  'Last backup %{when}': 'Letzte Sicherung %{when}',
  'No successful backup since %{when}': 'Keine erfolgreiche Sicherung seit %{when}',
  'Last successful backup %{when}': 'Letzte erfolgreiche Sicherung %{when}',
  'No backup has succeeded yet.': 'Bisher war keine Sicherung erfolgreich.',

  // Status board (views/SpaceStatus.vue).
  'All spaces': 'Alle Spaces',
  'A backup is running.': 'Eine Sicherung läuft.',
  'A restore is running.': 'Eine Wiederherstellung läuft.',
  'Old backups are being cleaned up.': 'Alte Sicherungen werden aufgeräumt.',
  'Started %{when}.': 'Gestartet %{when}.',
  'Could not refresh the status. Retrying…':
    'Der Status konnte nicht aktualisiert werden. Neuer Versuch…',
  'No successful backup since %{when}.': 'Keine erfolgreiche Sicherung seit %{when}.',
  'The last backup failed:': 'Die letzte Sicherung ist fehlgeschlagen:',
  'Last successful backup': 'Letzte erfolgreiche Sicherung',
  'None yet': 'Noch keine',
  'Next backup': 'Nächste Sicherung',
  'Scheduled backups are off': 'Geplante Sicherungen sind ausgeschaltet',
  'Not scheduled': 'Nicht geplant',
  'Keeps backups for': 'Bewahrt Sicherungen auf',
  'Back up now': 'Jetzt sichern',
  'Recent activity': 'Letzte Aktivitäten',

  // Retention (components/RetentionEditor.vue).
  '%{days} days': '%{days} Tage',
  Change: 'Ändern',
  'Keep backups for (days)': 'Sicherungen aufbewahren (Tage)',
  'At least %{days} days. A longer history gives you more time to notice that files were damaged or encrypted before the last good copy is gone.':
    'Mindestens %{days} Tage. Je länger, desto mehr Zeit bleibt Ihnen, beschädigte oder verschlüsselte Dateien zu bemerken, bevor die letzte gute Kopie verschwindet.',
  'Backups must be kept for at least %{days} days.':
    'Sicherungen müssen mindestens %{days} Tage aufbewahrt werden.',
  'Enter a whole number of days.': 'Geben Sie eine ganze Zahl von Tagen ein.',
  Save: 'Speichern',
  Cancel: 'Abbrechen',

  // Run history (components/RunHistory.vue).
  'Nothing has run yet.': 'Bisher ist noch nichts gelaufen.',
  Backup: 'Sicherung',
  Restore: 'Wiederherstellung',
  'Clean-up of old backups': 'Aufräumen alter Sicherungen',
  Succeeded: 'Erfolgreich',
  Failed: 'Fehlgeschlagen',
  Waiting: 'Wartet',
  'started by hand': 'von Hand gestartet',
  scheduled: 'geplant',
  '%{deleted} removed, %{kept} kept': '%{deleted} entfernt, %{kept} behalten',
  '%{files} files, %{size}': '%{files} Dateien, %{size}'
}

export const translations: Translations = { de }
