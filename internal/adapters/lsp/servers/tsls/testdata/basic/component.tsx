declare namespace JSX {
    interface IntrinsicElements {
        badge: { children?: string };
    }
}

import ModuleBucket, { defaultLabel } from "./module_contracts";

export type ComponentProps = {
    readonly label?: string;
};

export default function FixtureBadge({ label = defaultLabel }: ComponentProps) {
    const bucket = new ModuleBucket(label);

    return <badge>{bucket.describe()}</badge>;
}
