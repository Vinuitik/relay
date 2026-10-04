package com.relay.app.ui.components

import android.content.ActivityNotFoundException
import android.content.Intent
import android.net.Uri
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
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
import androidx.compose.material.icons.outlined.ContentPaste
import androidx.compose.material.icons.outlined.ErrorOutline
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
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
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import com.relay.app.model.KnownRunner
import com.relay.app.network.ClaudeAuthFinishRequest
import com.relay.app.network.RelayApiClient
import com.relay.app.network.friendlyErrorMessage
import com.relay.app.ui.theme.RelayMotion
import com.relay.app.ui.theme.codeSmall
import com.relay.app.ui.theme.statusColors
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import org.json.JSONObject
import retrofit2.HttpException
import java.util.UUID

/** Where the phone-side Claude sign-in is. */
sealed interface ClaudeSignInState {
    data object Starting : ClaudeSignInState
    /** Runner is waiting for the code from [url]. [inlineError] = fixable without restarting. */
    data class AwaitingCode(
        val url: String,
        val finishing: Boolean = false,
        val inlineError: String? = null,
    ) : ClaudeSignInState
    data class SignedIn(val email: String?) : ClaudeSignInState
    data class Failed(val message: String) : ClaudeSignInState
}

/**
 * Drives `POST /v1/auth/claude/start` → (user signs in on the phone's browser) →
 * `POST /v1/auth/claude/finish {code}` → `GET /v1/auth/claude` for the email. One instance per
 * sheet opening (keyed in [ClaudeSignInSheet]), so a reopened sheet always starts fresh.
 */
class ClaudeSignInViewModel(private val runner: KnownRunner) : ViewModel() {
    private val api = RelayApiClient.forRunner(runner)

    var state by mutableStateOf<ClaudeSignInState>(ClaudeSignInState.Starting)
        private set
    var code by mutableStateOf("")

    init { start() }

    /** Also "Try again": the runner restarts its login process on every start. */
    fun start() {
        state = ClaudeSignInState.Starting
        code = ""
        viewModelScope.launch {
            state = try {
                val r = api.claudeAuthStart()
                val url = r.url
                if (url.isNullOrBlank()) {
                    ClaudeSignInState.Failed(r.message ?: "${runner.label} didn't return a sign-in page.")
                } else {
                    ClaudeSignInState.AwaitingCode(url)
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                ClaudeSignInState.Failed(explain(e))
            }
        }
    }

    fun finish() {
        val current = state as? ClaudeSignInState.AwaitingCode ?: return
        val trimmed = code.trim()
        if (trimmed.isEmpty()) {
            state = current.copy(inlineError = "Paste the code from the sign-in page first.")
            return
        }
        state = current.copy(finishing = true, inlineError = null)
        viewModelScope.launch {
            state = try {
                val r = api.claudeAuthFinish(ClaudeAuthFinishRequest(trimmed))
                if (r.state == "signed_in") {
                    // Email is a nicety - a failed status call still means signed in.
                    val email = r.email ?: runCatching { api.claudeAuthStatus() }.getOrNull()?.email
                    ClaudeSignInState.SignedIn(email)
                } else {
                    ClaudeSignInState.Failed(r.message ?: "Claude didn't accept that code.")
                }
            } catch (e: CancellationException) {
                throw e
            } catch (e: HttpException) {
                if (e.code() == 400) current.copy(finishing = false, inlineError = "Paste the code from the sign-in page first.")
                else ClaudeSignInState.Failed(explain(e))
            } catch (e: Exception) {
                ClaudeSignInState.Failed(explain(e))
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
            503 -> "${runner.label} can't find the claude CLI, so it can't sign in. Install Claude " +
                "Code there or put claude on the runner's PATH, then restart the runner." +
                (reason?.let { "\n\n$it" } ?: "")
            409 -> "This sign-in expired on ${runner.label} before the code arrived. Start again."
            else -> reason ?: friendlyErrorMessage(e, runner)
        }
    }
}

/**
 * Phone-side `claude auth login` for [runner]: start → open the sign-in page → paste the code →
 * finish. [onSignedIn] fires once with the account email (null if unknown); the caller decides
 * whether to close the sheet (Chat does, with a Snackbar) or leave the success state up.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ClaudeSignInSheet(
    runner: KnownRunner,
    onDismiss: () -> Unit,
    onSignedIn: (email: String?) -> Unit,
) {
    // Fresh ViewModel per opening: the key changes every time this composable enters composition.
    val openId = remember { UUID.randomUUID().toString() }
    val vm: ClaudeSignInViewModel = viewModel(
        key = "claudeSignIn-$openId",
        factory = viewModelFactory { initializer { ClaudeSignInViewModel(runner) } },
    )
    val haptics = LocalHapticFeedback.current
    val state = vm.state

    LaunchedEffect(state) {
        if (state is ClaudeSignInState.SignedIn) {
            haptics.performHapticFeedback(HapticFeedbackType.LongPress)
            onSignedIn(state.email)
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
            Text("Sign in to Claude", style = MaterialTheme.typography.titleLarge)
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
                    ClaudeSignInState.Starting -> StartingStep(runner)
                    is ClaudeSignInState.AwaitingCode -> CodeStep(vm, s)
                    is ClaudeSignInState.SignedIn -> SignedInStep(s.email, onDismiss)
                    is ClaudeSignInState.Failed -> FailedStep(s.message, onRetry = vm::start)
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

@Composable
private fun CodeStep(vm: ClaudeSignInViewModel, s: ClaudeSignInState.AwaitingCode) {
    val context = LocalContext.current
    val clipboard = LocalClipboardManager.current
    var openFailed by remember { mutableStateOf(false) }

    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        StepLabel("1", "Open the Claude sign-in page")
        FilledTonalButton(
            onClick = {
                openFailed = try {
                    context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(s.url)))
                    false
                } catch (_: ActivityNotFoundException) {
                    true
                }
            },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Icon(Icons.AutoMirrored.Filled.OpenInNew, contentDescription = null, modifier = Modifier.size(18.dp))
            Text("Open sign-in page", modifier = Modifier.padding(start = 8.dp))
        }
        if (openFailed) {
            Text(
                "No browser to open it. Copy this link into one:\n${s.url}",
                style = MaterialTheme.typography.codeSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }

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

@Composable
private fun StepLabel(number: String, text: String) {
    Text(
        "$number  $text",
        style = MaterialTheme.typography.titleSmall,
        modifier = Modifier.padding(top = 4.dp),
    )
}

@Composable
private fun SignedInStep(email: String?, onDone: () -> Unit) {
    val success = MaterialTheme.statusColors.success
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Icon(Icons.Filled.CheckCircle, contentDescription = null, tint = success.fg)
            Text(
                (email?.let { "Signed in as $it" } ?: "Signed in") + " - send your message again",
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
