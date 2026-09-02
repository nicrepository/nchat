import { beforeEach, describe, expect, it } from "vitest";

import { readDevicePreference, writeDevicePreference } from "./callDevicePreference";

const STORAGE_KEY = "nchat.call.device-preference.v1";

beforeEach(() => {
  localStorage.clear();
});

describe("callDevicePreference", () => {
  it("returns an empty preference when nothing is stored", () => {
    expect(readDevicePreference()).toEqual({});
  });

  it("round-trips a written preference per kind", () => {
    writeDevicePreference("audioinput", "mic-1");
    writeDevicePreference("videoinput", "cam-1");

    expect(readDevicePreference()).toEqual({ audioinput: "mic-1", videoinput: "cam-1" });
  });

  it("overwrites only the kind being written, keeping the others", () => {
    writeDevicePreference("audioinput", "mic-1");
    writeDevicePreference("audioinput", "mic-2");

    expect(readDevicePreference()).toEqual({ audioinput: "mic-2" });
  });

  it("never throws when localStorage is unavailable", () => {
    const original = globalThis.localStorage;
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      get() {
        throw new Error("storage disabled");
      },
    });

    expect(() => writeDevicePreference("audioinput", "mic-1")).not.toThrow();
    expect(readDevicePreference()).toEqual({});

    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      value: original,
    });
  });

  it("ignores malformed stored JSON", () => {
    localStorage.setItem(STORAGE_KEY, "not json");
    expect(readDevicePreference()).toEqual({});
  });

  it("ignores a stored value that is not an object", () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(["audioinput", "mic-1"]));
    expect(readDevicePreference()).toEqual({});
  });

  it("ignores a non-string or oversized device id for a kind", () => {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ audioinput: 42, videoinput: "x".repeat(600), audiooutput: "speaker-1" }),
    );
    expect(readDevicePreference()).toEqual({ audiooutput: "speaker-1" });
  });

  it("never stores an unknown kind", () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({ audioinput: "mic-1", bogus: "x" }));
    expect(readDevicePreference()).toEqual({ audioinput: "mic-1" });
  });
});
