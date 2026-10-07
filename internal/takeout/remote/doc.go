// Package remote is the target-facing half of the Take-Out: publishing a
// Space's key envelopes to the S3 target during a backup, and extracting a
// Space's ciphertext from it into a Take-Out directory (Path A, step 1).
//
// It is kept apart from package takeout, which holds the Take-Out format
// (manifest, layout, verification), so that the offline decrypt tool can read
// that format without linking any S3 client (phase 10.6). Everything that
// needs the network to reach a target belongs here, not in takeout.
package remote
