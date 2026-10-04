package com.relay.app.network

import com.relay.app.model.BrowseResult
import com.relay.app.model.AuthStatus
import com.relay.app.model.GitBranch
import com.relay.app.model.GitCommit
import com.relay.app.model.GitDiff
import com.relay.app.model.GitStatus
import com.relay.app.model.ContainersStatus
import com.relay.app.model.Device
import com.relay.app.model.FileContent
import com.relay.app.model.FileEntry
import com.relay.app.model.Project
import com.relay.app.model.RunnerInfo
import com.relay.app.model.Session
import com.relay.app.model.UsageReport
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
/** [runnerRef] = the address this phone uses for the runner (its `hostname`); echoed back in every
 * push so a notification can deep-link to the right runner. */
data class DeviceRegistrationRequest(val fcmToken: String, val runnerRef: String? = null)
data class StartContainersRequest(val file: String)
data class SetModeRequest(val modeId: String)
data class SetConfigRequest(val configId: String, val value: String)
data class PermissionRequest(val optionId: String)
data class GitPathsRequest(val paths: List<String> = emptyList(), val all: Boolean = false)
data class GitCommitRequest(val message: String)
data class GitSwitchRequest(val branch: String, val create: Boolean = false, val remote: Boolean = false)
/** The code a paste-code sign-in page shows after login, pasted on the phone. */
data class AuthFinishRequest(val code: String)

/**
 * Retrofit mirror of shared/API.md's v1 endpoint table. Endpoints whose response body carries no
 * data worth parsing (message — `202 {}`) return
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

    /** Sessions across every project (Home's "Needs you"). [state] = comma list, e.g.
     * `"waiting,busy"`; null = every state. Runner sorts waiting first, then busy, newest first. */
    @GET("v1/sessions")
    suspend fun listAllSessions(@Query("state") state: String? = null): List<Session>

    @GET("v1/sessions/{sessionId}")
    suspend fun getSession(@Path("sessionId") sessionId: String): Session

    @POST("v1/sessions/{sessionId}/message")
    suspend fun sendMessage(
        @Path("sessionId") sessionId: String,
        @Body request: MessageRequest,
    ): Response<ResponseBody>

    @POST("v1/sessions/{sessionId}/stop")
    suspend fun stopSession(@Path("sessionId") sessionId: String): Session

    /** Stops the agent's current turn without ending the session (the red Stop button). */
    @POST("v1/sessions/{sessionId}/cancel")
    suspend fun cancelTurn(@Path("sessionId") sessionId: String): Session

    /** Switches permission mode, one of [Session.modes]. */
    @POST("v1/sessions/{sessionId}/mode")
    suspend fun setMode(@Path("sessionId") sessionId: String, @Body request: SetModeRequest): Session

    /** Sets one of [Session.configOptions] (model, effort, fast). Also becomes the runner's default
     * for new sessions. */
    @POST("v1/sessions/{sessionId}/config")
    suspend fun setConfig(@Path("sessionId") sessionId: String, @Body request: SetConfigRequest): Session

    /** Answers [Session.pendingPermission] with one of its options. */
    @POST("v1/sessions/{sessionId}/permission")
    suspend fun answerPermission(
        @Path("sessionId") sessionId: String,
        @Body request: PermissionRequest,
    ): Session

    /** Detected compose files, the active one, and container states. Cheap - safe to poll. */
    @GET("v1/projects/{projectId}/containers")
    suspend fun containers(@Path("projectId") projectId: String): ContainersStatus

    /** Turns on [StartContainersRequest.file]; if another file was active the runner brings it
     * down first (a switch). Returns immediately (`202`) - poll [containers] until `operation`
     * is empty. */
    @POST("v1/projects/{projectId}/containers/start")
    suspend fun startContainers(
        @Path("projectId") projectId: String,
        @Body request: StartContainersRequest,
    ): ContainersStatus

    @POST("v1/projects/{projectId}/containers/stop")
    suspend fun stopContainers(@Path("projectId") projectId: String): ContainersStatus

    /** Registers/updates this phone's FCM push token with this runner. Response body carries a
     * typed [Device] per shared/API.md, but no current caller needs it beyond success/failure. */
    @POST("v1/devices")
    suspend fun registerDevice(@Body request: DeviceRegistrationRequest): Device

    /** Manual "suspend now" — see shared/API.md: `409` if a session is busy, `503` if the runner
     * wasn't started with RELAY_IDLE_SUSPEND_ENABLED=true. Called straight from
     * [com.relay.app.ui.screens.RunnerListScreen]'s "Sleep" button. */
    @POST("v1/suspend")
    suspend fun suspend(): Response<ResponseBody>

    /** "The app is in the foreground" - sent every 30s while it is (MainActivity), never in the
     * background. Feeds idle-suspend and the usage recorder's `app` signal. */
    @POST("v1/activity")
    suspend fun activity(): Response<ResponseBody>

    /** Recorded usage over the last [days] + idle-suspend simulation - see
     * [com.relay.app.ui.screens.UsageScreen]. */
    @GET("v1/usage")
    suspend fun usage(@Query("days") days: Int): UsageReport

    /** Lists a directory within a project — `path` omitted/blank means the project root. See
     * shared/API.md and [com.relay.app.ui.screens.FileBrowserContent]. Read-only. */
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

    /** Git tab (shared/API.md "Git"). `isRepo:false` for a plain folder - not an error. */
    @GET("v1/projects/{projectId}/git")
    suspend fun gitStatus(@Path("projectId") projectId: String): GitStatus

    @GET("v1/projects/{projectId}/git/diff")
    suspend fun gitDiff(
        @Path("projectId") projectId: String,
        @Query("path") path: String,
        @Query("staged") staged: Boolean,
    ): GitDiff

    @GET("v1/projects/{projectId}/git/log")
    suspend fun gitLog(@Path("projectId") projectId: String, @Query("limit") limit: Int = 30): List<GitCommit>

    @GET("v1/projects/{projectId}/git/branches")
    suspend fun gitBranches(@Path("projectId") projectId: String): List<GitBranch>

    /** Local actions answer with fresh [GitStatus]; git's refusals are `409 {error}`, bad input `400`. */
    @POST("v1/projects/{projectId}/git/stage")
    suspend fun gitStage(@Path("projectId") projectId: String, @Body request: GitPathsRequest): GitStatus

    @POST("v1/projects/{projectId}/git/unstage")
    suspend fun gitUnstage(@Path("projectId") projectId: String, @Body request: GitPathsRequest): GitStatus

    @POST("v1/projects/{projectId}/git/commit")
    suspend fun gitCommit(@Path("projectId") projectId: String, @Body request: GitCommitRequest): GitStatus

    @POST("v1/projects/{projectId}/git/switch")
    suspend fun gitSwitch(@Path("projectId") projectId: String, @Body request: GitSwitchRequest): GitStatus

    /** Network actions: `202` + status with `operation` set; poll [gitStatus] until it's "". */
    @POST("v1/projects/{projectId}/git/push")
    suspend fun gitPush(@Path("projectId") projectId: String): GitStatus

    @POST("v1/projects/{projectId}/git/pull")
    suspend fun gitPull(@Path("projectId") projectId: String): GitStatus

    @POST("v1/projects/{projectId}/git/fetch")
    suspend fun gitFetch(@Path("projectId") projectId: String): GitStatus

    /** Every sign-in the runner can relay, with login state (sign-in relay). */
    @GET("v1/auth")
    suspend fun authList(): List<AuthStatus>

    /** One provider's login state on the runner (+ account when signed in). */
    @GET("v1/auth/{provider}")
    suspend fun authStatus(@Path("provider") provider: String): AuthStatus

    /** Starts the provider's CLI login on the runner; returns `awaiting_code` + [AuthStatus.url]
     * (paste-code) or `awaiting_approval` + url + userCode (device flow). `503` = CLI not found,
     * `404` = unknown provider. */
    @POST("v1/auth/{provider}/start")
    suspend fun authStart(@Path("provider") provider: String): AuthStatus

    /** Hands the pasted code to the waiting sign-in (paste-code providers only). `400` empty code
     * or device-flow provider, `409` no sign-in in progress, `503` CLI not found. Returns
     * `signed_in` or `failed` + message. */
    @POST("v1/auth/{provider}/finish")
    suspend fun authFinish(@Path("provider") provider: String, @Body request: AuthFinishRequest): AuthStatus
}
