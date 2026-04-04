import { FIXTURE_STAMP, FixtureBucket, makeBucket } from "./fixture";

export function useBucket(): string {
    const left = new FixtureBucket(FIXTURE_STAMP);
    const right = makeBucket("secondary");

    return `${left.describe()}|${right.describe()}`;
}
