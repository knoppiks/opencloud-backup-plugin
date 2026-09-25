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
  '%{files} files, %{size}': '%{files} Dateien, %{size}',

  // Setup entry points (status/setupaction.ts).
  'Set up backup': 'Sicherung einrichten',
  'Finish setup': 'Einrichtung abschließen',
  'Turn on scheduled backups': 'Geplante Sicherungen einschalten',
  'A manager of this space has to finish setup.':
    'Eine verwaltende Person dieses Space muss die Einrichtung abschließen.',

  // Setup wizard (views/SetupWizard.vue, components/ActionError.vue).
  'Back to the space': 'Zurück zum Space',
  'Set up backup for %{space}': 'Sicherung für %{space} einrichten',
  'Only editors and managers of this space can set up backup.':
    'Nur Personen, die diesen Space bearbeiten oder verwalten, können die Sicherung einrichten.',
  'Where should backups go?': 'Wohin sollen die Sicherungen gehen?',
  'Backup destination': 'Sicherungsziel',
  'Saving…': 'Wird gespeichert…',
  Continue: 'Weiter',
  'The backup destination is chosen. Creating the Recovery Key needs a manager of this space.':
    'Das Sicherungsziel ist ausgewählt. Den Wiederherstellungsschlüssel muss eine verwaltende Person dieses Space erstellen.',
  'Your Recovery Key': 'Ihr Wiederherstellungsschlüssel',
  'The Recovery Key shown before was not taken into use. Throw away any copy of it; a new one will be created.':
    'Der zuvor angezeigte Wiederherstellungsschlüssel wurde nicht übernommen. Vernichten Sie jede Kopie davon; es wird ein neuer erstellt.',
  'Creating the Recovery Key did not work. Nothing was saved. Please try again.':
    'Der Wiederherstellungsschlüssel konnte nicht erstellt werden. Es wurde nichts gespeichert. Bitte versuchen Sie es erneut.',
  'Your backups are locked with a key. Next, a Recovery Key is created for you. You need it to read your backups if this server is ever lost, and nobody else can give it back to you.':
    'Ihre Sicherungen werden mit einem Schlüssel verschlossen. Als Nächstes wird ein Wiederherstellungsschlüssel für Sie erstellt. Sie brauchen ihn, um Ihre Sicherungen zu lesen, falls dieser Server einmal verloren geht, und niemand sonst kann ihn Ihnen zurückgeben.',
  'Creating it takes a few seconds, and the page may not respond while it does.':
    'Das Erstellen dauert einige Sekunden, und die Seite reagiert währenddessen eventuell nicht.',
  'Create my Recovery Key': 'Meinen Wiederherstellungsschlüssel erstellen',
  'Creating your Recovery Key. This takes a moment…':
    'Ihr Wiederherstellungsschlüssel wird erstellt. Das dauert einen Moment…',
  'Save your Recovery Key now': 'Speichern Sie jetzt Ihren Wiederherstellungsschlüssel',
  'This is the only time it is shown. Save it in your password manager, or write it down and keep it somewhere safe.':
    'Er wird nur dieses eine Mal angezeigt. Speichern Sie ihn in Ihrem Passwortmanager, oder schreiben Sie ihn auf und bewahren Sie ihn an einem sicheren Ort auf.',
  'Without it, nobody — not even your administrator — can read your backups if this server is lost.':
    'Ohne ihn kann niemand – auch nicht Ihre Administration – Ihre Sicherungen lesen, falls dieser Server verloren geht.',
  'Recovery Key': 'Wiederherstellungsschlüssel',
  Copy: 'Kopieren',
  Copied: 'Kopiert',
  'I have saved it': 'Ich habe ihn gespeichert',
  'Check that you saved it': 'Prüfen, ob Sie ihn gespeichert haben',
  'Type two groups from your saved Recovery Key. This makes sure you can find it when you need it.':
    'Geben Sie zwei Gruppen aus Ihrem gespeicherten Wiederherstellungsschlüssel ein. So ist sichergestellt, dass Sie ihn finden, wenn Sie ihn brauchen.',
  'Group %{number}': 'Gruppe %{number}',
  'That does not match. Look at your saved copy and try again.':
    'Das stimmt nicht überein. Sehen Sie in Ihrer gespeicherten Kopie nach und versuchen Sie es erneut.',
  'Show the key again': 'Schlüssel erneut anzeigen',
  'Protect this space': 'Diesen Space schützen',
  'It is not clear whether setup finished.':
    'Es ist unklar, ob die Einrichtung abgeschlossen wurde.',
  'Keep the Recovery Key you saved. Checking again finds out whether this space now uses it.':
    'Behalten Sie den gespeicherten Wiederherstellungsschlüssel. Eine erneute Prüfung zeigt, ob dieser Space ihn jetzt verwendet.',
  'Check again': 'Erneut prüfen',
  'This space is already protected': 'Dieser Space ist bereits geschützt',
  'Its backups are already locked with a Recovery Key. Setting it up again would make every existing backup unreadable, so that is not possible.':
    'Seine Sicherungen sind bereits mit einem Wiederherstellungsschlüssel verschlossen. Eine erneute Einrichtung würde alle vorhandenen Sicherungen unlesbar machen, deshalb ist sie nicht möglich.',
  'When should backups run?': 'Wann sollen Sicherungen laufen?',
  'How often': 'Wie oft',
  'Every day': 'Jeden Tag',
  'Once a week': 'Einmal pro Woche',
  Day: 'Tag',
  Time: 'Uhrzeit',
  'Times are in the server’s time zone (%{zone}).':
    'Zeiten gelten in der Zeitzone des Servers (%{zone}).',
  'Times are in the server’s time zone.': 'Zeiten gelten in der Zeitzone des Servers.',
  'Turn on backups': 'Sicherungen einschalten',
  'Backup is set up': 'Die Sicherung ist eingerichtet',
  'Backups run automatically. You can check on them at any time.':
    'Sicherungen laufen automatisch. Sie können jederzeit nachsehen, wie es um sie steht.',
  'First backup: %{when}': 'Erste Sicherung: %{when}',
  'The first backup runs at the next scheduled time.':
    'Die erste Sicherung läuft zum nächsten geplanten Zeitpunkt.',
  'Keep your Recovery Key safe. It is the only way to read these backups if this server is lost.':
    'Bewahren Sie Ihren Wiederherstellungsschlüssel sicher auf. Nur mit ihm lassen sich diese Sicherungen lesen, falls dieser Server verloren geht.',
  // Restore (8d.3)
  'Restore files': 'Dateien wiederherstellen',
  'Restore files in %{space}': 'Dateien in %{space} wiederherstellen',
  'Restore files from a backup': 'Dateien aus einer Sicherung wiederherstellen',
  'Backups are not set up for this space yet, so there is nothing to restore.':
    'Für diesen Space ist noch keine Sicherung eingerichtet, daher gibt es nichts wiederherzustellen.',
  'That backup is no longer available. Older backups are removed as they reach the end of the keep period. Please choose another one.':
    'Diese Sicherung ist nicht mehr vorhanden. Ältere Sicherungen werden entfernt, sobald ihre Aufbewahrungszeit abgelaufen ist. Bitte wählen Sie eine andere.',
  'There are no backups to restore yet. The first one appears after the first backup has run.':
    'Es gibt noch keine Sicherung zum Wiederherstellen. Die erste erscheint, sobald die erste Sicherung gelaufen ist.',
  'Which backup do you want back?': 'Welche Sicherung möchten Sie zurückholen?',
  'Restore the backup from %{when}?': 'Die Sicherung von %{when} wiederherstellen?',
  'The files are copied into a new folder inside “Restore” in this space. Nothing in the space is changed or overwritten.':
    'Die Dateien werden in einen neuen Ordner innerhalb von „Restore“ in diesem Space kopiert. Im Space wird nichts verändert oder überschrieben.',
  'The copy takes up %{size} of this space’s storage.':
    'Die Kopie belegt %{size} vom Speicherplatz dieses Spaces.',
  'Choose another backup': 'Andere Sicherung wählen',
  'Another backup or restore is running for this space':
    'Für diesen Space läuft bereits eine Sicherung oder Wiederherstellung',
  'Try again when it has finished.': 'Versuchen Sie es erneut, wenn sie abgeschlossen ist.',
  'Restoring the backup from %{when}…': 'Die Sicherung von %{when} wird wiederhergestellt…',
  'A restore is running for this space.': 'Für diesen Space läuft eine Wiederherstellung.',
  'The files are being copied into:': 'Die Dateien werden kopiert nach:',
  'You can leave this page. The restore carries on, and the space’s recent activity shows when it has finished.':
    'Sie können diese Seite verlassen. Die Wiederherstellung läuft weiter, und die letzten Aktivitäten des Spaces zeigen, wann sie abgeschlossen ist.',
  'Restore finished': 'Wiederherstellung abgeschlossen',
  'Your files are in:': 'Ihre Dateien liegen in:',
  'Restore another backup': 'Eine weitere Sicherung wiederherstellen',
  'The restore did not finish': 'Die Wiederherstellung wurde nicht abgeschlossen',
  'Your backups are unaffected.': 'Ihre Sicherungen sind davon nicht betroffen.',
  'Anything restored before it stopped is in:':
    'Was bis zum Abbruch wiederhergestellt wurde, liegt in:',
  'This restore can no longer be followed here. The space’s recent activity shows how it ended.':
    'Diese Wiederherstellung lässt sich hier nicht mehr verfolgen. Die letzten Aktivitäten des Spaces zeigen, wie sie ausgegangen ist.',
  'Restored into:': 'Wiederhergestellt nach:',
  'Restoring into:': 'Wird wiederhergestellt nach:',

  // Recovery Key: check, key file and replacement (8d.4).
  'Recovery Key for %{space}': 'Wiederherstellungsschlüssel für %{space}',
  'This space has no Recovery Key yet. It is created when backup is set up.':
    'Dieser Space hat noch keinen Wiederherstellungsschlüssel. Er wird beim Einrichten der Sicherung erstellt.',
  'Check my Recovery Key': 'Meinen Wiederherstellungsschlüssel prüfen',
  'Make sure the Recovery Key you saved still opens this space’s backups. It is checked on this device only and is not sent anywhere.':
    'Prüfen Sie, ob Ihr gespeicherter Wiederherstellungsschlüssel die Sicherungen dieses Spaces noch öffnet. Die Prüfung findet nur auf diesem Gerät statt; der Schlüssel wird nirgendwohin gesendet.',
  Check: 'Prüfen',
  'Checking. This takes a moment…': 'Wird geprüft. Das dauert einen Moment…',
  'This Recovery Key opens this space’s backups. Keep it safe.':
    'Dieser Wiederherstellungsschlüssel öffnet die Sicherungen dieses Spaces. Bewahren Sie ihn sicher auf.',
  'This is not a Recovery Key. Check it for typing mistakes: a Recovery Key has seven groups of letters and numbers.':
    'Das ist kein Wiederherstellungsschlüssel. Prüfen Sie ihn auf Tippfehler: Ein Wiederherstellungsschlüssel besteht aus sieben Gruppen aus Buchstaben und Ziffern.',
  'This Recovery Key does not open this space’s backups.':
    'Dieser Wiederherstellungsschlüssel öffnet die Sicherungen dieses Spaces nicht.',
  'Check that it is the key for this space and that it was copied in full.':
    'Prüfen Sie, ob es der Schlüssel für diesen Space ist und ob er vollständig kopiert wurde.',
  'The key could not be checked. This says nothing about whether it is right.':
    'Der Schlüssel konnte nicht geprüft werden. Das sagt nichts darüber aus, ob er richtig ist.',
  'Key file': 'Schlüsseldatei',
  'If this server is ever lost, your administrator can give you a copy of your backups, and the decrypt program restores it with your Recovery Key. Keep this key file with your Recovery Key: it is locked, and useless without it.':
    'Falls dieser Server einmal verloren geht, kann Ihre Administration Ihnen eine Kopie Ihrer Sicherungen geben, und das Programm „decrypt“ stellt sie mit Ihrem Wiederherstellungsschlüssel wieder her. Bewahren Sie diese Schlüsseldatei zusammen mit Ihrem Wiederherstellungsschlüssel auf: Sie ist verschlossen und ohne ihn nutzlos.',
  'Download recovery.ocbke': 'recovery.ocbke herunterladen',
  'Downloaded. Download it again after the Recovery Key is replaced.':
    'Heruntergeladen. Laden Sie die Datei erneut herunter, nachdem der Wiederherstellungsschlüssel ersetzt wurde.',
  'Replace the Recovery Key': 'Wiederherstellungsschlüssel ersetzen',
  'Replace the Recovery Key for %{space}': 'Wiederherstellungsschlüssel für %{space} ersetzen',
  'If the Recovery Key is lost': 'Wenn der Wiederherstellungsschlüssel verloren ist',
  'Nobody can give it back. There is no copy anywhere, not even with your administrator.':
    'Niemand kann ihn zurückgeben. Es gibt nirgendwo eine Kopie, auch nicht bei Ihrer Administration.',
  'Backups and restores here keep working without it. It is needed only if this server is lost.':
    'Sicherungen und Wiederherstellungen hier funktionieren auch ohne ihn weiter. Gebraucht wird er nur, wenn dieser Server verloren geht.',
  'Setting up backup again is not possible, because that would make every existing backup unreadable. A new Recovery Key can only be made with the current one.':
    'Die Sicherung erneut einzurichten ist nicht möglich, weil das alle vorhandenen Sicherungen unlesbar machen würde. Ein neuer Wiederherstellungsschlüssel lässt sich nur mit dem aktuellen erstellen.',
  'Back to the Recovery Key': 'Zurück zum Wiederherstellungsschlüssel',
  'Only a manager of this space can replace its Recovery Key, because every member who kept the old one would need the new one.':
    'Nur eine verwaltende Person dieses Spaces kann seinen Wiederherstellungsschlüssel ersetzen, denn alle Mitglieder, die den alten aufbewahrt haben, bräuchten dann den neuen.',
  'The new Recovery Key shown before was not taken into use. Throw away any copy of it. The current Recovery Key still works.':
    'Der zuvor angezeigte neue Wiederherstellungsschlüssel wurde nicht übernommen. Vernichten Sie jede Kopie davon. Der aktuelle Wiederherstellungsschlüssel gilt weiterhin.',
  'A new Recovery Key replaces the current one. Existing backups stay readable, and nothing is uploaded again.':
    'Ein neuer Wiederherstellungsschlüssel ersetzt den aktuellen. Vorhandene Sicherungen bleiben lesbar, und nichts wird erneut hochgeladen.',
  'To make the new key, enter the current Recovery Key.':
    'Geben Sie den aktuellen Wiederherstellungsschlüssel ein, um den neuen zu erstellen.',
  'Current Recovery Key': 'Aktueller Wiederherstellungsschlüssel',
  'Creating the new Recovery Key did not work. Nothing was changed. Please try again.':
    'Der neue Wiederherstellungsschlüssel konnte nicht erstellt werden. Es wurde nichts geändert. Bitte versuchen Sie es erneut.',
  'The stored key file could not be read, so the key could not be tried. Nothing was changed.':
    'Die gespeicherte Schlüsseldatei konnte nicht gelesen werden, deshalb ließ sich der Schlüssel nicht prüfen. Es wurde nichts geändert.',
  'This takes a few seconds, and the page may not respond while it does.':
    'Das dauert einige Sekunden, und die Seite reagiert währenddessen möglicherweise nicht.',
  'Creating your new Recovery Key. This takes a moment…':
    'Ihr neuer Wiederherstellungsschlüssel wird erstellt. Das dauert einen Moment…',
  'Save your new Recovery Key now': 'Speichern Sie jetzt Ihren neuen Wiederherstellungsschlüssel',
  'Nothing has changed yet. The current Recovery Key still works.':
    'Noch hat sich nichts geändert. Der aktuelle Wiederherstellungsschlüssel gilt weiterhin.',
  'It is not clear whether the Recovery Key was replaced.':
    'Es ist unklar, ob der Wiederherstellungsschlüssel ersetzt wurde.',
  'Keep both the current and the new Recovery Key for now. Checking again finds out which one this space uses.':
    'Behalten Sie vorerst sowohl den aktuellen als auch den neuen Wiederherstellungsschlüssel. Eine erneute Prüfung zeigt, welchen dieser Space verwendet.',
  'Someone else replaced the Recovery Key':
    'Jemand anderes hat den Wiederherstellungsschlüssel ersetzt',
  'The Recovery Key of this space was replaced by someone else while you were doing the same. The key shown to you here was not taken into use: throw away any copy of it.':
    'Der Wiederherstellungsschlüssel dieses Spaces wurde von jemand anderem ersetzt, während Sie dasselbe getan haben. Der Ihnen hier angezeigte Schlüssel wurde nicht übernommen: Vernichten Sie jede Kopie davon.',
  'Ask the other managers of this space for the new Recovery Key.':
    'Fragen Sie die anderen verwaltenden Personen dieses Spaces nach dem neuen Wiederherstellungsschlüssel.',
  'The Recovery Key is replaced': 'Der Wiederherstellungsschlüssel ist ersetzt',
  'Existing backups stay readable with the new key, and nothing was uploaded again.':
    'Vorhandene Sicherungen bleiben mit dem neuen Schlüssel lesbar, und nichts wurde erneut hochgeladen.',
  'The old Recovery Key stops working once the next backup has run. After that, destroy every copy of it.':
    'Der alte Wiederherstellungsschlüssel verliert seine Gültigkeit, sobald die nächste Sicherung gelaufen ist. Vernichten Sie danach jede Kopie davon.',
  'Other members of this space who kept the old Recovery Key need the new one. If you downloaded the key file before, download it again.':
    'Andere Mitglieder dieses Spaces, die den alten Wiederherstellungsschlüssel aufbewahrt haben, brauchen den neuen. Falls Sie die Schlüsseldatei schon einmal heruntergeladen haben, laden Sie sie erneut herunter.',
  'A backup is running. When it has finished, the old Recovery Key stops working.':
    'Eine Sicherung läuft. Wenn sie abgeschlossen ist, verliert der alte Wiederherstellungsschlüssel seine Gültigkeit.'
}

export const translations: Translations = { de }
