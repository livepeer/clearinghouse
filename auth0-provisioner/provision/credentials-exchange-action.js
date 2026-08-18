/**
 * Auth0 Credentials Exchange Action — copy claims from token request into access token.
 * Deploy: auth0-provisioner/provision/bootstrap-credentials-exchange-action.sh
 */
exports.onExecuteCredentialsExchange = async (event, api) => {
  const externalUserId = event.request?.body?.external_user_id;
  const clientId = event.request?.body?.client_id;
  if (!externalUserId || !clientId) {
    return;
  }
  api.accessToken.setCustomClaim("external_user_id", externalUserId);
  api.accessToken.setCustomClaim("app_client_id", clientId);
};
