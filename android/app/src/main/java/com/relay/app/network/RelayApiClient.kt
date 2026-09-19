package com.relay.app.network

import com.relay.app.model.KnownRunner
import com.squareup.moshi.Moshi
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.logging.HttpLoggingInterceptor
import retrofit2.Retrofit
import retrofit2.converter.moshi.MoshiConverterFactory
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException

/**
 * Turns a raw network exception into something a non-technical reader can act on, instead of a
 * bare exception message like "Failed to connect to /100.124.46.7 (port 7777) after 1000ms:
 * ECONNREFUSED" - that message actually IS a fast connection refusal (not a slow timeout the
 * number suggests), typically caused by either the runner not running, or - confirmed by hand
 * once already, 2026-09-19 - a host firewall silently dropping inbound connections from other
 * Tailscale peers even though the runner works fine when tested from its own machine. Every
 * screen that calls a [RelayApiService] method should route its catch block's exception through
 * this instead of using `e.message` directly.
 */
fun friendlyErrorMessage(e: Exception, runner: KnownRunner): String {
    val where = "${runner.label} (${runner.hostname}:${runner.port})"
    return when (e) {
        is ConnectException, is SocketTimeoutException ->
            "Can't reach $where. Check: is the runner actually running right now? Is this " +
                "phone's Tailscale connected? Does the runner's machine allow inbound " +
                "connections on port ${runner.port} (a host firewall can silently block " +
                "other devices even though the runner works fine from its own machine)."
        is UnknownHostException ->
            "Can't resolve $where - is this phone's Tailscale connected?"
        else -> "$where: ${e.message ?: e::class.simpleName}"
    }
}

/**
 * Builds (and caches) a [RelayApiService] per known runner. Every request carries the
 * `X-Relay-Key` header from that runner's stored key (see ARCHITECTURE.md "Registration") — auth
 * is entirely this header plus the Tailscale tunnel, no OAuth/session state.
 *
 * Plain `http://` per the task's networking spec; ARCHITECTURE.md describes the tailnet as the
 * real transport security boundary (TLS termination is explicitly out of scope for v1).
 */
object RelayApiClient {

    private val cache = mutableMapOf<String, RelayApiService>()

    fun forRunner(runner: KnownRunner): RelayApiService {
        val cacheKey = "${runner.hostname}:${runner.port}:${runner.key}"
        return cache.getOrPut(cacheKey) { build(runner) }
    }

    private fun build(runner: KnownRunner): RelayApiService {
        val authInterceptor = Interceptor { chain ->
            val authed = chain.request().newBuilder()
                .addHeader("X-Relay-Key", runner.key)
                .build()
            chain.proceed(authed)
        }
        val logging = HttpLoggingInterceptor().apply {
            level = HttpLoggingInterceptor.Level.BASIC
        }
        val client = OkHttpClient.Builder()
            .addInterceptor(authInterceptor)
            .addInterceptor(logging)
            .build()
        val moshi = Moshi.Builder().add(KotlinJsonAdapterFactory()).build()

        return Retrofit.Builder()
            .baseUrl("http://${runner.hostname}:${runner.port}/")
            .client(client)
            .addConverterFactory(MoshiConverterFactory.create(moshi))
            .build()
            .create(RelayApiService::class.java)
    }
}
