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
    val state: String, // "busy" | "idle" | "waiting" | "finished" | "error"
    val createdAt: String,
    val finishedAt: String?,
    val messages: List<Message>,
    // ACP providers only (e.g. claude): permission modes and an open permission request.
    val mode: String? = null,
    val modes: List<AgentMode>? = null,
    val pendingPermission: PendingPermission? = null,
)

data class Message(
    val role: String, // "user" | "agent" | "tool"
    val text: String, // for "tool": a one-line title like "Write hello.txt"
    val at: String,
    val toolKind: String? = null, // read, edit, execute, search, ...
    val status: String? = null, // tool only: pending, in_progress, completed, failed
)

data class AgentMode(
    val id: String, // e.g. "default", "acceptEdits", "bypassPermissions"
    val name: String,
    val description: String? = null,
)

data class PendingPermission(
    val title: String,
    val toolKind: String?,
    val options: List<PermissionOption>,
)

data class PermissionOption(
    val optionId: String,
    val name: String,
    val kind: String, // allow_once, allow_always, reject_once, reject_always
)

data class RunnerInfo(
    val hostname: String,
    val busy: Boolean,
    val version: String,
    // This runner's LAN network in CIDR form (e.g. "192.168.1.0/24"), null if undetectable.
    val localSubnet: String? = null,
)

/** A runner the phone has been manually pointed at (see ARCHITECTURE.md "Registration"). */
data class KnownRunner(
    val hostname: String,
    val port: Int = DEFAULT_PORT,
    val key: String,
    // Human-readable label shown in the UI instead of `hostname` (a Tailscale address like
    // "100.124.46.7" - not something a person should have to read as a name). Typed by the user
    // at pairing time (blank falls back to the hostname) and never recomputed afterward, so it
    // stays stable even if `hostname` changes. Null for any runner added before this field
    // existed (absent JSON key decodes to null via Moshi) - [label] falls back to `hostname`.
    val displayName: String? = null,
) {
    /** What to show in the UI - the display name if set, else the raw hostname/address. */
    val label: String get() = displayName?.takeIf { it.isNotBlank() } ?: hostname

    companion object {
        const val DEFAULT_PORT = 7777
    }
}

/** Mirrors shared/API.md's `Device` type — response body of `POST /v1/devices`. */
data class Device(
    val id: String,
    val fcmToken: String,
    val registeredAt: String,
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
    /** One level up; `""` = the drive list (Windows); null at the top. */
    val parent: String? = null,
    val entries: List<DirEntry>,
)

/** Response of every `/v1/projects/{id}/containers` endpoint - see
 * [com.relay.app.ui.screens.ContainersScreen]. */
data class ContainersStatus(
    val composeFiles: List<String>,
    val activeFile: String, // "" = several files and none chosen yet
    val hasDockerfile: Boolean,
    val containers: List<ContainerInfo>,
    val operation: String, // "" | "starting" | "switching" | "stopping"
    val lastError: String,
    val dockerError: String,
)

data class ContainerInfo(
    val service: String,
    val state: String, // docker's state: "running", "exited", ...
    val composeFile: String,
)
