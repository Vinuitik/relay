package com.relay.app.network

import com.relay.app.model.Device
import com.relay.app.model.Project
import com.relay.app.model.RunnerInfo
import com.relay.app.model.Session
import okhttp3.ResponseBody
import retrofit2.Response
import retrofit2.http.Body
import retrofit2.http.GET
import retrofit2.http.POST
import retrofit2.http.Path

data class HealthResponse(val ok: Boolean)
data class NewProjectRequest(val name: String)
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

    @GET("v1/projects/{projectId}/sessions")
    suspend fun listSessions(@Path("projectId") projectId: String): List<Session>

    @POST("v1/projects/{projectId}/sessions")
    suspend fun createSession(
        @Path("projectId") projectId: String,
        @Body request: NewSessionRequest,
    ): Session

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
}
