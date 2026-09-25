import { expect, it } from "vitest";
import { queryGateAll } from "./queryGate";

it("null 是合法的就绪数据，类型与运行时都不排除", () => {
  const gate = queryGateAll({ data: null as { value: number } | null, error: null });
  if (!gate.ready) throw new Error("not ready");
  // @ts-expect-error 就绪只排除 undefined，null 不能赋给非空类型
  const declaredNonNull: { value: number } = gate.data[0];
  expect(declaredNonNull).toBeNull();
});

it("元组位置与输入查询一一对应", () => {
  const gate = queryGateAll({ data: "s" as string | undefined, error: null }, { data: 1 as number | undefined, error: null });
  if (!gate.ready) throw new Error("not ready");
  const text: string = gate.data[0];
  const count: number = gate.data[1];
  expect([text, count]).toEqual(["s", 1]);
});
