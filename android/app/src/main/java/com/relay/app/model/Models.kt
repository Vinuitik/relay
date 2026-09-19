package com.relay.app.model

/**
 * Data classes mirroring shared/API.md's wire types exactly (field names/types). Moshi's
 * reflection-based Kotlin adapter (moshi-kotlin) serializes/deserializes these directly — no
 * @JsonClass codegen needed for a skeleton this small.
 */

data class Project(
    val id: String,
    val name: String,
    val path: String,
    val createdAt: String,
)

data class Session(
    val id: String,
    val projectId: String,
    val provider: String,
    val state: String, // "busy" | "idle" | "finished" | "error"
    val createdAt: String,
    val finishedAt: String?,
    val messages: List<Message>,
)

data class Message(
    val role: String, // "user" | "agent"
    val text: String,
    val at: String,
)

data class RunnerInfo(
    val hostname: String,
    val busy: Boolean,
    val version: String,
    // This runner's LAN network in CIDR form (e.g. "192.168.1.0/24"), null if undetectable.
    // Used to auto-match which two known runners share a physical LAN for Wake-on-LAN — see
    // [com.relay.app.data.WakeViaMatcher].
    val localSubnet: String? = null,
)

/** Mirrors shared/API.md's `UptimeInterval` — one entry from `GET /v1/uptime`. */
data class UptimeInterval(
    val start: String,
    val end: String?,
)

/** A runner the phone has been manually pointed at (see ARCHITECTURE.md "Registration"). */
data class KnownRunner(
    val hostname: String,
    val port: Int = DEFAULT_PORT,
    val key: String,
    // Human-readable label shown in the UI instead of `hostname` (a Tailscale address like
    // "100.124.46.7" - not something a person should have to read as a name, see
    // [com.relay.app.data.FriendlyNameGenerator]). Set once at pairing time (typed by the user,
    // or a generated "Adjective Noun" default if left blank) and never recomputed afterward, so
    // it stays stable even if `hostname` changes. Null for any runner added before this field
    // existed (absent JSON key decodes to null via Moshi) - [label] falls back to `hostname`.
    val displayName: String? = null,
    // Wake-on-LAN config for waking THIS runner's machine when it's fully off (see
    // ARCHITECTURE.md "Relay device"). Both null until set via the edit affordance on
    // RunnerListScreen. There's no separate "id" concept in this skeleton — `hostname` is
    // already the unique key (see KnownRunnersRepository), so `wakeViaRunnerId` stores another
    // known runner's `hostname`. Nullable fields decode fine via Moshi for entries persisted
    // before this migration (absent JSON keys default to null).
    val wakeMac: String? = null,
    val wakeViaRunnerId: String? = null,
) {
    /** What to show in the UI - the display name if set, else the raw hostname/address. */
    val label: String get() = displayName?.takeIf { it.isNotBlank() } ?: hostname

    companion object {
        const val DEFAULT_PORT = 8080
    }
}

/** Mirrors shared/API.md's `Device` type — response body of `POST /v1/devices`. */
data class Device(
    val id: String,
    val fcmToken: String,
    val registeredAt: String,
)

/** One project's outcome within `POST /v1/containers/{start,stop}-all` — see shared/API.md. */
data class ContainerActionResult(
    val projectId: String,
    val ok: Boolean,
    val error: String? = null,
)

/** One entry from `GET /v1/projects/{id}/files` — a file or subdirectory. */
data class FileEntry(
    val name: String,
    val isDir: Boolean,
    val size: Long,
)

/** Response body of `GET /v1/projects/{id}/files/content` — see shared/API.md. */
data class FileContent(
    val path: String,
    val content: String,
)

/** One directory entry from `GET /v1/browse` — see [com.relay.app.ui.screens.FolderPickerScreen]. */
data class DirEntry(
    val name: String,
)

/** Response body of `GET /v1/browse` — unscoped filesystem browsing for picking a project
 * directory to register, unlike [FileEntry]'s project-scoped listing. */
data class BrowseResult(
    val path: String,
    val entries: List<DirEntry>,
)
