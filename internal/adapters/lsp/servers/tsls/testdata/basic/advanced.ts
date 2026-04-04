import { createModuleBucket as buildModuleBucket } from "./module_contracts";
import { defaultBucket as ReexportedBucket } from "./module_reexports";
import type { ReexportedShape } from "./module_reexports";
import * as moduleNamespace from "./module_reexports";

function logged<This, Args extends unknown[], Return>(
    value: (this: This, ...args: Args) => Return,
    _context: ClassMethodDecoratorContext<
        This,
        (this: This, ...args: Args) => Return
    >,
) {
    return value;
}

export type ExtendedShape<TLabel extends string> = ReexportedShape<TLabel> & {
    readonly note?: string;
};

export interface LoaderOptions {
    readonly label?: string;
    readonly count?: number;
}

export declare namespace FixtureAmbient {
    interface Config {
        readonly label?: string;
    }
}

export class DerivedBucket extends ReexportedBucket<string> {
    @logged
    public override describe(): string {
        return `derived:${super.describe()}`;
    }
}

export function createAdvancedBucket(label: string): DerivedBucket;
export function createAdvancedBucket(shape: ExtendedShape<string>): DerivedBucket;
export function createAdvancedBucket(
    input: string | ExtendedShape<string>,
): DerivedBucket {
    const { label } = typeof input === "string" ? { label: input } : input;

    return new DerivedBucket(label);
}

export async function loadBuckets(
    { label = moduleNamespace.defaultLabel, count = 1 }: LoaderOptions = {},
): Promise<DerivedBucket[]> {
    const createFromNamespace = () => moduleNamespace.aliasBucket(label);
    const buckets = await Promise.resolve(
        Array.from({ length: count }, () => createFromNamespace()),
    );

    return buckets.map((bucket) => new DerivedBucket(bucket.label));
}

export namespace AdvancedRegistry {
    export const current = createAdvancedBucket(
        buildModuleBucket(moduleNamespace.defaultLabel).label,
    );
}
