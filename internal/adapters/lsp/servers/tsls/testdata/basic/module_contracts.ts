export interface ModuleShape<TLabel extends string = string> {
    readonly label: TLabel;
}

export default class ModuleBucket<TLabel extends string = string> {
    public constructor(
        public readonly label: TLabel,
    ) { }

    public describe(): string {
        return this.label;
    }
}

export function createModuleBucket<TLabel extends string>(
    label: TLabel,
): ModuleBucket<TLabel> {
    return new ModuleBucket(label);
}

export const defaultLabel = "module-default";
