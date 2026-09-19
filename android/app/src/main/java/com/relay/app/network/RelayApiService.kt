package com.relay.app.network

import com.relay.app.model.BrowseResult
import com.relay.app.model.ContainerActionResult
import com.relay.app.model.Device
import com.relay.app.model.FileContent
import com.relay.app.model.FileEntry
import com.relay.app.model.Project
import com.relay.app.model.RunnerInfo
import com.relay.app.model.Session
import com.relay.app.model.UptimeInterval
import okhttp3.ResponseBody
import retrofit2.Response
import retrofit2.http.Body
import retrofit2.http.GET
import retrofit2.http.POST
import retrofit2.http.Path
import retrofit2.http.Query

data class HealthResponse(val ok: Boolean)
data class NewProjectRequest(val name: String, val path: String? = null)
data class NewSessionRequest(val provider: String)
data class MessageRequest(val text: String)
data class WakeRequest(val mac: String)
data class DeviceRegistrationRequest(val fcmToken: String)

/**
 * Retrofit mirror of shared/API.md's v1 endpoint table. Endpoints whose response body carries no
 * data worth parsing (message/containers start/stop — `202 {}` / `200 {}`) return
 * `Response<ResponseBody>` rather than a typed/Unit body: Retrofit has no built-in converter for
 * bare `Unit`, and `ResponseBody` is always supported without needing one — callers just check
 * `isSuccessful` and close the body.
 */
interface RelayApiService {

    @GET("v1/health")
    suspend fun health(): HealthResponse

    @GET("v1/runner/info")
    suspend fun runnerInfo(): RunnerInfo

    @GET("v1/projects")
    suspend fun listProjects(): List<Project>

    @POST("v1/projects")
    suspend fun createProject(@Body request: NewProjectRequest): Project

    /** Lists subdirectories of an arbitrary absolute path, unscoped — used to pick a project
     * directory before it's registered, see [com.relay.app.ui.screens.FolderPickerScreen].
     * `path` omitted/blank lists filesystem roots. */
    @GET("v1/browse")
    suspend fun browse(@Query("path") path: String = ""): BrowseResult

    @GET("v1/projects/{projectId}/sessions")
    suspend fun listSessions(@Path("projectId") projectId: String): List<Session>

    @POST("v1/projects/{projectId}/sessions")
    suspend fun createSession(
        @Path("projectId") projectId: String,
        @Body request: NewSessionRequest,
    ): Session

    /** Starts the `claude`/`codex` CLI's OAuth login as a session with no associated project —
     * see shared/API.md and [com.relay.app.ui.screens.AuthLoginScreen]. The OAuth URL shows up
     * as an "agent" message in the returned session; paste the code back via [sendMessage]. */
    @POST("v1/auth/login")
    suspend fun startAuthLogin(): Session

    @GET("v1/sessions/{sessionId}")
    suspend fun getSession(@Path("sessionId") sessionId: String): Session

    @POST("v1/sessions/{sessionId}/message")
    suspend fun sendMessage(
        @Path("sessionId") sessionId: String,
        @Body request: MessageRequest,
    ): Response<ResponseBody>

    @POST("v1/sessions/{sessionId}/stop")
    suspend fun stopSession(@Path("sessionId") sessionId: String): Session

    @POST("v1/projects/{projectId}/containers/start")
    suspend fun startContainers(@Path("projectId") projectId: String): Response<ResponseBody>

    @POST("v1/projects/{projectId}/containers/stop")
    suspend fun stopContainers(@Path("projectId") projectId: String): Response<ResponseBody>

    /** Starts/stops docker compose across every project this runner knows about, best-effort
     * per project — see shared/API.md and [com.relay.app.widget.ContainersAllWorker]. */
    @POST("v1/containers/start-all")
    suspend fun startAllContainers(): List<ContainerActionResult>

    @POST("v1/containers/stop-all")
    suspend fun stopAllContainers(): List<ContainerActionResult>

    /**
     * Broadcasts a WoL magic packet on THIS runner's local network. Only meaningful when called
     * on a runner that is on the same LAN as the (possibly fully-off) wake target — see
     * ARCHITECTURE.md "Relay device" and [com.relay.app.widget.WakeRunnerWorker].
     */
    @POST("v1/wake")
    suspend fun wake(@Body request: WakeRequest): Response<ResponseBody>

    /** Registers/updates this phone's FCM push token with this runner. Response body carries a
     * typed [Device] per shared/API.md, but no current caller needs it beyond success/failure. */
    @POST("v1/devices")
    suspend fun registerDevice(@Body request: DeviceRegistrationRequest): Device

    /** This runner's short-term up/down interval buffer — see shared/API.md and
     * [com.relay.app.data.UptimeSyncWorker], which pulls this into the app's own long-term Room
     * history. */
    @GET("v1/uptime")
    suspend fun uptime(): List<UptimeInterval>

    /** Manual "suspend now" — see shared/API.md: `409` if a session is busy, `503` if the runner
     * wasn't started with RELAY_IDLE_SUSPEND_ENABLED=true. See
     * [com.relay.app.widget.SuspendRunnerWorker]. */
    @POST("v1/suspend")
    suspend fun suspend(): Response<ResponseBody>

    /** Lists a directory within a project — `path` omitted/blank means the project root. See
     * shared/API.md and [com.relay.app.ui.screens.FileBrowserScreen]. Read-only. */
    @GET("v1/projects/{projectId}/files")
    suspend fun listFiles(
        @Path("projectId") projectId: String,
        @Query("path") path: String = "",
    ): List<FileEntry>

    /** Returns one file's text content — see shared/API.md for the `400`/`404`/`413`/`415`
     * refusal cases (path escape, missing, too large, binary). */
    @GET("v1/projects/{projectId}/files/content")
    suspend fun fileContent(
        @Path("projectId") projectId: String,
        @Query("path") path: String,
    ): FileContent
}
