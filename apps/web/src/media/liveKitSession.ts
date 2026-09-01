import type {
  CallDeviceKind,
  CallMediaDevice,
  LiveKitSpikeSession,
  LiveKitSpikeSessionCallbacks,
  LiveKitSpikeSessionFactory,
} from "../mediaSpike/liveKitSpikeSession";

export type LiveKitSession = LiveKitSpikeSession;
export type LiveKitSessionCallbacks = LiveKitSpikeSessionCallbacks;
export type LiveKitSessionFactory = LiveKitSpikeSessionFactory;
export type LiveKitSessionLoader = () => Promise<LiveKitSessionFactory>;
export type { CallDeviceKind, CallMediaDevice };

interface LiveKitSessionModule {
  createLiveKitSpikeSession: LiveKitSessionFactory;
}

export function createLiveKitSessionLoader(
  importer: () => Promise<LiveKitSessionModule> = () => import("../mediaSpike/liveKitSpikeSession"),
): LiveKitSessionLoader {
  let modulePromise: Promise<LiveKitSessionFactory> | undefined;

  return () => {
    modulePromise ??= importer()
      .then(({ createLiveKitSpikeSession }) => createLiveKitSpikeSession)
      .catch((error: unknown) => {
        modulePromise = undefined;
        throw error;
      });
    return modulePromise;
  };
}

export const loadLiveKitSessionFactory = createLiveKitSessionLoader();
