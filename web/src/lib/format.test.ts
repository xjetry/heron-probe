import { describe, expect, it } from "vitest";
import { ago, bytes, duration, formatUnit, percent } from "./format";

describe("format", () => {
  it("bytes 用二进制单位，小数只在个位数时出现", () => {
    expect(bytes(0)).toBe("0 B");
    expect(bytes(1023)).toBe("1023 B");
    expect(bytes(1024)).toBe("1.0 KiB");
    expect(bytes(15n * 1024n * 1024n)).toBe("15 MiB");
    expect(bytes(3.5 * 1024 ** 3)).toBe("3.5 GiB");
  });
  it("percent 小于 10 保留一位小数", () => {
    expect(percent(3.14159)).toBe("3.1%");
    expect(percent(42.6)).toBe("43%");
  });
  it("duration 取最大的两个单位", () => {
    expect(duration(59)).toBe("0m");
    expect(duration(3661)).toBe("1h 1m");
    expect(duration(90000n)).toBe("1d 1h");
  });
  it("ago 以 hub 的 now 为基准且不出现负数", () => {
    expect(ago(1000, 1002)).toBe("刚刚");
    expect(ago(1000, 1030)).toBe("30 秒前");
    expect(ago(1000, 1000 + 7200)).toBe("2 小时前");
    expect(ago(2000, 1000)).toBe("刚刚");
  });
  it("formatUnit 按单位分派，未知单位保留两位小数", () => {
    expect(formatUnit(50, "percent")).toBe("50%");
    expect(formatUnit(2048, "bytes")).toBe("2.0 KiB");
    expect(formatUnit(2.4, "count")).toBe("2");
    expect(formatUnit(1.23456, "")).toBe("1.23");
  });
});
