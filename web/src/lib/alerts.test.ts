import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChannelKind, NotifyChannelSchema } from "../gen/probe/v1/admin_pb";
import { CHANNEL_KINDS, channelTarget, labelOf } from "./alerts";

describe("labelOf", () => {
  it("表内值给标签，表外值显示原值而不抛错", () => {
    expect(labelOf(CHANNEL_KINDS, ChannelKind.WEBHOOK)).toBe("Webhook");
    expect(labelOf(CHANNEL_KINDS, 9 as ChannelKind)).toBe("未知（9）");
  });
});

describe("channelTarget", () => {
  it("Telegram 只显示会话", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.TELEGRAM, telegram: { chatId: "-100", hasBotToken: true } });
    expect(channelTarget(c)).toBe("会话 -100");
  });
  it("Webhook 显示方法、主机与头名，空方法按 POST", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.WEBHOOK, webhook: { method: "", hasUrl: true, urlHost: "https://hooks.example", headerNames: ["Authorization", "X-Tag"] } });
    expect(channelTarget(c)).toBe("POST https://hooks.example，头 Authorization、X-Tag");
  });
  it("Webhook 没有头时不带头名段", () => {
    const c = create(NotifyChannelSchema, { kind: ChannelKind.WEBHOOK, webhook: { method: "PUT", hasUrl: true, urlHost: "http://10.0.0.2:8080" } });
    expect(channelTarget(c)).toBe("PUT http://10.0.0.2:8080");
  });
});
