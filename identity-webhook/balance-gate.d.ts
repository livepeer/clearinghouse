import type {
  BalanceCheck,
  BalanceCheckContext,
  UsageIdentity,
} from "./protocol.js";

export type ExpiryTtl = Readonly<{
  seconds: number;
}>;

export function parseUsdMicros(
  value: bigint | number | string | null | undefined,
): bigint | null;

export function createBalanceGate(options: {
  getBalanceUsdMicros: (
    identity: UsageIdentity,
    ctx: BalanceCheckContext,
  ) =>
    | Promise<bigint | number | string | null | undefined>
    | bigint
    | number
    | string
    | null
    | undefined;
  minBalanceUsdMicros?: bigint | number | string;
  expiryTtl?: ExpiryTtl;
  failClosed?: boolean;
  onError?: (err: unknown, identity: UsageIdentity) => void;
}): BalanceCheck;
