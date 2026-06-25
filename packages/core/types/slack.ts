/** A Slack bot installation bound to a single Multica agent (MUL-3666).
 *
 * Wire shape mirrors `SlackInstallationResponse` in
 * `server/internal/handler/slack.go`. New fields the backend adds in the
 * future MUST default to optional so older desktop builds keep parsing the
 * response — see CLAUDE.md → API Compatibility. */
export interface SlackInstallation {
  id: string;
  workspace_id: string;
  agent_id: string;
  /** The Slack workspace (team) id this bot is installed in. */
  team_id: string;
  /** The installed bot's Slack user id. */
  bot_user_id: string;
  installer_user_id: string;
  status: "active" | "revoked" | string;
  installed_at: string;
  created_at: string;
  updated_at: string;
}

export interface ListSlackInstallationsResponse {
  installations: SlackInstallation[];
  /** Whether the deployment has the at-rest secret key configured. When false
   * the connect entry points are hidden and the panel renders an "ask the
   * operator to enable Slack" state. */
  configured: boolean;
  /** Whether the OAuth self-serve install path is wired (the hosted Slack
   * app's client credentials are set). When false, connect entry points are
   * hidden but already-installed bots stay manageable. Optional so an older
   * desktop build hitting a server that predates the field treats it as not
   * supported. */
  install_supported?: boolean;
}

/** The Slack OAuth begin response: the authorize URL the browser is sent to.
 * Unlike Feishu's device-flow QR, this is a redirect — Slack bounces back to
 * the backend callback, which lands the install and redirects to Settings. */
export interface BeginSlackInstallResponse {
  url: string;
}

/** Post-redemption echo: the Slack user id the token carried is now bound to
 * the logged-in Multica user in this workspace/installation. */
export interface RedeemSlackBindingTokenResponse {
  workspace_id: string;
  installation_id: string;
  slack_user_id: string;
}
