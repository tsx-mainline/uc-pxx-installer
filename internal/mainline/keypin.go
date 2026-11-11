package mainline

// The release's pin of the firmware key record (owner answer O3,
// 2026-11-10, MAINLINE-INT-2026-11-10.md §7; MAINLINE-INT-REVIEW-T2.md §5):
// the sha256 of the key record (package keykeep, record version 1) of the
// key of the reviewed 1.0.0.94 userdebug firmware (c9766c0a…), the only
// firmware a mainline install takes its boot chain from. The record is a
// deterministic function of the key and the IV, and its sha256 is in every
// plan, log and bundle already, so the pin exposes nothing new. It is never
// the key: the record bytes stay on the unit.
//
// A mainline plan keeps the key only when the record it keeps, or finds kept
// on the unit, has this sha256 (plan.mainlineKeepKey): a record kept by an
// earlier install is then proven by the pin alone, so a userdebug-first
// install whose unit holds the record in both sectors needs no .bin phase;
// a stash record is held to the same pin. Anything else refuses (fails
// closed). The pin is a field of the release's gate only: a test gate has
// none (its made-up firmware has another key), and a release build holds no
// test gate (ci/check-no-writer.py).

// ReviewedKeyRecordSHA256 is the sha256 of the key record of the 1.0.0.94
// userdebug firmware key (record version 1). A new pin is a reviewed data
// change (a new binary sha, an Opus review).
const ReviewedKeyRecordSHA256 = "7143b925e62e867ae6a428fafac17252d4e7b001c96767ee00c40f77f787b08e"

// KeyRecordPin is the gate's pin of the key record: ReviewedKeyRecordSHA256
// for the release's gate (EmbeddedGate), "" for a test gate and for nil.
func (g *Gate) KeyRecordPin() string {
	if g != nil && g.name == "embedded" {
		return ReviewedKeyRecordSHA256
	}
	return ""
}
