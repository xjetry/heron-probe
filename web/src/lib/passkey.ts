function decode(value: string): ArrayBuffer {
  const raw = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0)).buffer;
}

function encode(value: ArrayBuffer): string {
  return btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export async function passkeyCredential(optionsJSON: string, register = false): Promise<string> {
  if (!window.PublicKeyCredential || !navigator.credentials) throw new Error("此浏览器或当前来源不支持 Passkey，请使用 HTTPS。");
  const options = JSON.parse(optionsJSON);
  const key = options.publicKey;
  key.challenge = decode(key.challenge);
  for (const list of [key.allowCredentials, key.excludeCredentials]) {
    for (const credential of list ?? []) credential.id = decode(credential.id);
  }
  if (register) key.user.id = decode(key.user.id);
  const credential = (register
    ? await navigator.credentials.create({ publicKey: key })
    : await navigator.credentials.get({ publicKey: key })) as PublicKeyCredential | null;
  if (!credential) throw new Error("认证已取消。");
  const response = credential.response;
  const encoded: Record<string, unknown> = { clientDataJSON: encode(response.clientDataJSON) };
  if (response instanceof AuthenticatorAttestationResponse) {
    encoded.attestationObject = encode(response.attestationObject);
    encoded.transports = response.getTransports();
  } else {
    const assertion = response as AuthenticatorAssertionResponse;
    encoded.authenticatorData = encode(assertion.authenticatorData);
    encoded.signature = encode(assertion.signature);
    encoded.userHandle = assertion.userHandle ? encode(assertion.userHandle) : null;
  }
  return JSON.stringify({ id: credential.id, rawId: encode(credential.rawId), type: credential.type, response: encoded, clientExtensionResults: credential.getClientExtensionResults(), authenticatorAttachment: credential.authenticatorAttachment });
}
