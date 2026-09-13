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
)

/** A runner the phone has been manually pointed at (see ARCHITECTURE.md "Registration"). */
data class KnownRunner(
    val hostname: String,
    val port: Int = DEFAULT_PORT,
    val key: String,
) {
    companion object {
        const val DEFAULT_PORT = 8080
    }
}
