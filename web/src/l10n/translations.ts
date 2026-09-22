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
// **No extraction pipeline yet, deliberately.** With this many strings, a
// `.pot`/`.po` toolchain would be more moving parts than text. `translations.spec.ts`
// keeps the catalogue honest in the meantime: it fails when a msgid in the
// source has no German, and when a German entry no longer matches any msgid. 8d
// adds the pipeline, when there is enough text to justify it.

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
    'Das erledigt sich meist von selbst. Versuchen Sie es in einem Moment erneut.'
}

export const translations: Translations = { de }
