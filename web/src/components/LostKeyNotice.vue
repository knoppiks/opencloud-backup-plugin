<script setup lang="ts">
// What losing a Recovery Key means, said once (8d.4 decision 7).
//
// Shown on the check page, in its "does not open" answer and at the start of
// the replacement. The facts it states are design decisions, not wording
// choices: there is no escrow (key-envelope-format.md §5), setup cannot be run
// again (decisions.md #17), and a replacement needs the current key
// (decisions.md #18).
</script>

<template>
  <div data-testid="lost-key">
    <h3 class="ext:font-semibold">{{ $gettext('If the Recovery Key is lost') }}</h3>
    <p>
      {{
        $gettext(
          'Nobody can give it back. There is no copy anywhere, not even with your administrator.'
        )
      }}
    </p>
    <p>
      {{
        $gettext(
          'Backups and restores here keep working without it. It is needed only if this server is lost.'
        )
      }}
    </p>
    <p>
      {{
        $gettext(
          'Setting up backup again is not possible, because that would make every existing backup unreadable. A new Recovery Key can only be made with the current one.'
        )
      }}
    </p>
  </div>
</template>
