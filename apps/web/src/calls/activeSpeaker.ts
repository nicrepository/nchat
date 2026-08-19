export interface StableSpeakerState {
  candidate: string | null;
  since: number;
  visible: string | null;
}

export function nextStableSpeaker(
  previous: StableSpeakerState | null,
  candidate: string | null,
  now: number,
  delay: number,
): StableSpeakerState {
  if (!previous || previous.candidate !== candidate) {
    return { candidate, since: now, visible: previous?.visible ?? null };
  }
  if (now - previous.since < delay) return previous;
  if (previous.visible === candidate) return previous;
  return { ...previous, visible: candidate };
}
