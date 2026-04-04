export interface FixtureContract {
    describe(): string;
}

export enum FixtureStatus {
    Active = "active",
    Archived = "archived",
}

export const FIXTURE_STAMP = "ts-fixture";

export let fixtureCounter = 0;

export class FixtureBucket implements FixtureContract {
    public constructor(
        public readonly label: string,
        private readonly status: FixtureStatus = FixtureStatus.Active,
    ) { }

    public describe(): string {
        return `${this.label}:${this.status}`;
    }

    public bump(): number {
        fixtureCounter += 1;
        return fixtureCounter;
    }
}

export function makeBucket(label: string): FixtureBucket {
    return new FixtureBucket(label);
}
