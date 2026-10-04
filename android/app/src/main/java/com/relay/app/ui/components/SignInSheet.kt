package com.relay.app.ui.components

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.OpenInNew
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.outlined.ContentCopy
import androidx.compose.material.icons.outlined.ContentPaste
import androidx.compose.material.icons.outlined.ErrorOutline
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.relay.app.model.AuthStatus
import com.relay.app.model.KnownRunner
import com.relay.app.network.AuthFinishRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.theme.RelayMotion
import com.relay.app.ui.theme.codeSmall
import com.relay.app.ui.theme.statusColors
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import org.json.JSONObject
import retrofit2.HttpException
import java.util.UUID

/** Where a phone-side sign-in (sign-in relay, runner/FLOWS.md "Sign-in relay") is. */
sealed interface SignInState {
    data object Starting : SignInState
    /** Paste-code recipe: runner waits for the code from [url]. [inlineError] = fixable without restarting. */
    data class AwaitingCode(
        val url: String,
        val finishing: Boolean = false,
        val inlineError: String? = null,
    ) : SignInState
    /** Device flow: user enters [userCode] at [url]; the runner notices by itself, we poll. */
    data class AwaitingApproval(val url: String, val userCode: String) : SignInState
    data class SignedIn(val account: String?) : SignInState
    data class Failed(val message: String) : SignInState
}

/**
 * Drives `POST /v1/auth/{provider}/start` → either (paste-code) `POST …/finish {code}`, or
 * (device flow) polling `GET /v1/auth/{provider}` until signed_in/failed. One instance per sheet
 * opening (keyed in [SignInSheet]), so a reopened sheet always starts fresh.
 */
class SignInViewModel(
    private val runner: KnownRunner,
    private val provider: String,
    private val providerName: String,
) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var state by mutableStateOf<SignInState>(SignInState.Starting)
        private set
    var code by mutableStateOf("")

    init { start() }

    /** Also "Try again": the runner restarts its login process on every start. */
    fun start() {
        state = SignInState.Starting
        code = ""
        viewModelScope.launch {
            state = try {
                val r = api.authStart(provider)
                val url = r.url
                when {
                    url.isNullOrBlank() ->
                        SignInState.Failed(r.message ?: "${runner.label} didn't return a sign-in page.")
                    r.state == "awaiting_approval" && !r.userCode.isNullOrBlank() ->
                        SignInState.AwaitingApproval(url, r.userCode).also { pollApproval() }
                    else -> SignInState.AwaitingCode(url)
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                SignInState.Failed(explain(e))
            }
        }
    }

    /** Device flow: the runner's CLI finishes on its own once the user approved in the browser. */
    private fun pollApproval() {
        viewModelScope.launch {
            while (state is SignInState.AwaitingApproval) {
                delay(POLL_MS)
                val s = try {
                    api.authStatus(provider)
                } catch (e: CancellationException) {
                    throw e
                } catch (_: Exception) {
                    continue // transient - keep waiting; the runner's own timeout ends it
                }
                when (s.state) {
                    "signed_in" -> state = SignInState.SignedIn(s.account)
                    "failed" -> state = SignInState.Failed(s.message ?: "$providerName sign-in failed.")
                    "awaiting_approval" -> Unit
                    else -> state = SignInState.Failed("The sign-in on ${runner.label} stopped. Start again.")
                }
            }
        }
    }

    fun finish() {
        val current = state as? SignInState.AwaitingCode ?: return
        val trimmed = code.trim()
        if (trimmed.isEmpty()) {
            state = current.copy(inlineError = "Paste the code from the sign-in page first.")
            return
        }
        state = current.copy(finishing = true, inlineError = null)
        viewModelScope.launch {
            state = try {
                val r = api.authFinish(provider, AuthFinishRequest(trimmed))
                if (r.state == "signed_in") {
                    // Account is a nicety - a failed status call still means signed in.
                    val account = r.account ?: runCatching { api.authStatus(provider) }.getOrNull()?.account
                    SignInState.SignedIn(account)
                } else {
                    SignInState.Failed(r.message ?: "$providerName didn't accept that code.")
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: HttpException) {
                if (e.code() == 400) current.copy(finishing = false, inlineError = "Paste the code from the sign-in page first.")
                else SignInState.Failed(explain(e))
            } catch (e: Exception) {
                SignInState.Failed(explain(e))
            }
        }
    }

    /** The runner's documented refusals get plain wording; the rest go through [friendlyErrorMessage]. */
    private fun explain(e: Exception): String {
        if (e !is HttpException) return friendlyErrorMessage(e, runner)
        val reason = runCatching {
            JSONObject(e.response()?.errorBody()?.string().orEmpty()).optString("error").takeIf { it.isNotBlank() }
        }.getOrNull()
        return when (e.code()) {
            503 -> "${runner.label} can't find the $providerName CLI, so it can't sign in. Install it " +
                "there or put it on the runner's PATH, then restart the runner." +
                (reason?.let { "\n\n$it" } ?: "")
            409 -> "This sign-in expired on ${runner.label} before the code arrived. Start again."
            404 -> "${runner.label} doesn't know how to sign in to $providerName - update the runner."
            else -> reason ?: friendlyErrorMessage(e, runner)
        }
    }

    private companion object {
        const val POLL_MS = 2_000L
    }
}

/**
 * Phone-side sign-in to [provider] ("claude", "github", "gcloud", …) on [runner]. [onSignedIn]
 * fires once with the account (null if unknown); the caller decides whether to close the sheet
 * (Chat does, with a Snackbar) or leave the success state up. [doneHint] is appended to the
 * success line (Chat: "send your message again").
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SignInSheet(
    runner: KnownRunner,
    provider: String,
    providerName: String,
    onDismiss: () -> Unit,
    onSignedIn: (account: String?) -> Unit,
    doneHint: String? = null,
) {
    // Fresh ViewModel per opening: the key changes every time this composable enters composition.
    val openId = remember { UUID.randomUUID().toString() }
    val vm: SignInViewModel = viewModel(
        key = "signIn-$provider-$openId",
        factory = viewModelFactory { initializer { SignInViewModel(runner, provider, providerName) } },
    )
    val haptics = LocalHapticFeedback.current
    val state = vm.state

    LaunchedEffect(state) {
        if (state is SignInState.SignedIn) {
            haptics.performHapticFeedback(HapticFeedbackType.LongPress)
            onSignedIn(state.account)
        }
    }

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .imePadding()
                .padding(start = 24.dp, end = 24.dp, bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text("Sign in to $providerName", style = MaterialTheme.typography.titleLarge)
            Text(
                "On ${runner.label}",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            AnimatedContent(
                targetState = state::class,
                transitionSpec = {
                    fadeIn(tween(RelayMotion.DurationMedium, easing = RelayMotion.EaseOut)) togetherWith
                        fadeOut(tween(RelayMotion.DurationShort, easing = RelayMotion.EaseOut))
                },
                label = "signInStep",
            ) { _ ->
                when (val s = vm.state) {
                    SignInState.Starting -> StartingStep(runner)
                    is SignInState.AwaitingCode -> CodeStep(vm, s, providerName)
                    is SignInState.AwaitingApproval -> ApprovalStep(s)
                    is SignInState.SignedIn -> SignedInStep(s.account, doneHint, onDismiss)
                    is SignInState.Failed -> FailedStep(s.message, onRetry = vm::start)
                }
            }
        }
    }
}

@Composable
private fun StartingStep(runner: KnownRunner) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(vertical = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        CircularProgressIndicator(modifier = Modifier.size(24.dp), strokeWidth = 2.dp)
        Text("Starting sign-in on ${runner.label}…", style = MaterialTheme.typography.bodyMedium)
    }
}

/** Opens [url] in a browser; returns false when there's no browser. */
private fun openUrl(context: android.content.Context, url: String): Boolean = try {
    context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url)))
    true
} catch (_: ActivityNotFoundException) {
    false
}

@Composable
private fun CodeStep(vm: SignInViewModel, s: SignInState.AwaitingCode, providerName: String) {
    val context = LocalContext.current
    val clipboard = LocalClipboardManager.current
    var openFailed by remember { mutableStateOf(false) }

    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        StepLabel("1", "Open the $providerName sign-in page")
        FilledTonalButton(onClick = { openFailed = !openUrl(context, s.url) }, modifier = Modifier.fillMaxWidth()) {
            Icon(Icons.AutoMirrored.Filled.OpenInNew, contentDescription = null, modifier = Modifier.size(18.dp))
            Text("Open sign-in page", modifier = Modifier.padding(start = 8.dp))
        }
        if (openFailed) NoBrowserHint(s.url)

        StepLabel("2", "Sign in, then copy the code the page shows")
        OutlinedTextField(
            value = vm.code,
            onValueChange = { vm.code = it },
            modifier = Modifier.fillMaxWidth(),
            label = { Text("Paste code") },
            singleLine = true,
            enabled = !s.finishing,
            textStyle = MaterialTheme.typography.codeSmall,
            isError = s.inlineError != null,
            supportingText = s.inlineError?.let { { Text(it) } },
            keyboardOptions = KeyboardOptions(
                autoCorrect = false,
                keyboardType = KeyboardType.Ascii,
                imeAction = ImeAction.Done,
            ),
            keyboardActions = KeyboardActions(onDone = { vm.finish() }),
            trailingIcon = {
                IconButton(
                    enabled = !s.finishing,
                    onClick = { clipboard.getText()?.text?.trim()?.let { vm.code = it } },
                ) { Icon(Icons.Outlined.ContentPaste, contentDescription = "Paste") }
            },
        )
        Button(
            onClick = vm::finish,
            enabled = !s.finishing && vm.code.isNotBlank(),
            modifier = Modifier.fillMaxWidth(),
        ) {
            if (s.finishing) {
                CircularProgressIndicator(
                    modifier = Modifier.size(18.dp),
                    strokeWidth = 2.dp,
                    color = MaterialTheme.colorScheme.onPrimary,
                )
                Text("Signing in…", modifier = Modifier.padding(start = 8.dp))
            } else {
                Text("Finish sign-in")
            }
        }
    }
}

/** Device flow: show the one-time code; "Copy code & open" puts it on the clipboard first. */
@Composable
private fun ApprovalStep(s: SignInState.AwaitingApproval) {
    val context = LocalContext.current
    val clipboard = LocalClipboardManager.current
    var openFailed by remember { mutableStateOf(false) }

    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        StepLabel("1", "Your one-time code")
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                s.userCode,
                style = MaterialTheme.typography.headlineMedium,
                modifier = Modifier.weight(1f),
            )
            IconButton(onClick = { clipboard.setText(AnnotatedString(s.userCode)) }) {
                Icon(Icons.Outlined.ContentCopy, contentDescription = "Copy code")
            }
        }
        StepLabel("2", "Enter it on the page and approve")
        FilledTonalButton(
            onClick = {
                clipboard.setText(AnnotatedString(s.userCode))
                openFailed = !openUrl(context, s.url)
            },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Icon(Icons.AutoMirrored.Filled.OpenInNew, contentDescription = null, modifier = Modifier.size(18.dp))
            Text("Copy code & open page", modifier = Modifier.padding(start = 8.dp))
        }
        if (openFailed) NoBrowserHint(s.url)
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            modifier = Modifier.padding(top = 4.dp),
        ) {
            CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
            Text(
                "Waiting for approval - this finishes by itself.",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun NoBrowserHint(url: String) {
    Text(
        "No browser to open it. Copy this link into one:\n$url",
        style = MaterialTheme.typography.codeSmall,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
    )
}

@Composable
private fun StepLabel(number: String, text: String) {
    Text(
        "$number  $text",
        style = MaterialTheme.typography.titleSmall,
        modifier = Modifier.padding(top = 4.dp),
    )
}

@Composable
private fun SignedInStep(account: String?, doneHint: String?, onDone: () -> Unit) {
    val success = MaterialTheme.statusColors.success
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Icons.Filled.CheckCircle, contentDescription = null, tint = success.fg)
            Text(
                (account?.let { "Signed in as $it" } ?: "Signed in") + (doneHint?.let { " - $it" } ?: ""),
                style = MaterialTheme.typography.bodyLarge,
            )
        }
        TextButton(onClick = onDone, modifier = Modifier.align(Alignment.End)) { Text("Done") }
    }
}

@Composable
private fun FailedStep(message: String, onRetry: () -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Icons.Outlined.ErrorOutline, contentDescription = null, tint = MaterialTheme.colorScheme.error)
            Text(message, style = MaterialTheme.typography.bodyMedium)
        }
        Button(onClick = onRetry, modifier = Modifier.fillMaxWidth()) { Text("Try again") }
    }
}

/** `GET /v1/auth` for one runner: every sign-in it can relay. */
class SignInsViewModel(runner: KnownRunner) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)
    private val label = runner.label

    var providers by mutableStateOf<List<AuthStatus>?>(null)
        private set
    var error by mutableStateOf<String?>(null)
        private set

    init { refresh() }

    fun refresh() {
        viewModelScope.launch {
            try {
                providers = api.authList()
                error = null
            } catch (e: CancellationException) {
                throw e
            } catch (e: HttpException) {
                error = if (e.code() == 404) "$label's runner is too old for this - update it." else e.message()
            } catch (e: Exception) {
                error = e.message ?: "Couldn't reach $label."
            }
        }
    }
}

/**
 * Runner row ⋮ → "Sign-ins": one row per provider (status + account), tap → [SignInSheet] on top.
 * A provider whose CLI isn't installed on the runner is shown disabled.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SignInsSheet(runner: KnownRunner, onDismiss: () -> Unit) {
    val openId = remember { UUID.randomUUID().toString() }
    val vm: SignInsViewModel = viewModel(
        key = "signIns-$openId",
        factory = viewModelFactory { initializer { SignInsViewModel(runner) } },
    )
    var signingIn by remember { mutableStateOf<AuthStatus?>(null) }

    ModalBottomSheet(
        onDismissRequest = onDismiss,
        sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true),
    ) {
        Column(
            modifier = Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text("Sign-ins", style = MaterialTheme.typography.titleLarge)
            Text(
                "On ${runner.label}",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            val list = vm.providers
            when {
                list == null && vm.error == null -> StartingStep(runner)
                list == null -> FailedStep(vm.error!!, onRetry = vm::refresh)
                else -> list.forEachIndexed { i, p ->
                    if (i > 0) HorizontalDivider()
                    ProviderRow(p, onClick = { signingIn = p })
                }
            }
        }
    }

    signingIn?.let { p ->
        SignInSheet(
            runner = runner,
            provider = p.provider,
            providerName = p.name,
            onDismiss = { signingIn = null; vm.refresh() },
            onSignedIn = { vm.refresh() },
        )
    }
}

@Composable
private fun ProviderRow(p: AuthStatus, onClick: () -> Unit) {
    val success = MaterialTheme.statusColors.success
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .clickable(enabled = p.cliFound, onClick = onClick)
            .padding(vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(p.name, style = MaterialTheme.typography.titleMedium)
            Text(
                when {
                    !p.cliFound -> "Not installed on this runner"
                    p.loggedIn -> p.account ?: "Signed in"
                    else -> "Not signed in"
                },
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        when {
            !p.cliFound -> Unit
            p.loggedIn -> Icon(Icons.Filled.CheckCircle, contentDescription = "Signed in", tint = success.fg)
            else -> TextButton(onClick = onClick) { Text("Sign in") }
        }
    }
}
