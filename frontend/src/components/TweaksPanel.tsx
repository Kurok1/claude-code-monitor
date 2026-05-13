import { useEffect, useState } from 'react';
import { Icon } from './Icon';

export type Density = 'compact' | 'regular' | 'comfy';
export type Accent = 'orange' | 'blue' | 'green';

export interface Tweaks {
  density: Density;
  accent: Accent;
  dark: boolean;
}

export const ACCENT_MAP: Record<Accent, Record<string, string>> = {
  orange: {
    '--accent': '#D97757',
    '--accent-hover': '#C4643E',
    '--accent-bg': '#FDF3EE',
    '--accent-100': '#F9DFD1',
    '--accent-200': '#F0C5A8',
    '--accent-300': '#E5A57E',
  },
  blue: {
    '--accent': '#3B6FD4',
    '--accent-hover': '#2A56AB',
    '--accent-bg': '#EEF4FF',
    '--accent-100': '#D5E2FA',
    '--accent-200': '#A8C0EE',
    '--accent-300': '#7396DD',
  },
  green: {
    '--accent': '#2D7D46',
    '--accent-hover': '#1E5730',
    '--accent-bg': '#EDFBF2',
    '--accent-100': '#C8EBD3',
    '--accent-200': '#9CD4AF',
    '--accent-300': '#6BB785',
  },
};

const STORAGE_KEY = 'ccm.tweaks';

const DEFAULTS: Tweaks = { density: 'regular', accent: 'orange', dark: false };

export function loadTweaks(): Tweaks {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULTS;
    const parsed = JSON.parse(raw) as Partial<Tweaks>;
    return { ...DEFAULTS, ...parsed };
  } catch {
    return DEFAULTS;
  }
}

export function applyTweaks(t: Tweaks): void {
  document.body.dataset.density = t.density;
  document.body.dataset.theme = t.dark ? 'dark' : 'light';
  const accent = ACCENT_MAP[t.accent] || ACCENT_MAP.orange;
  for (const [k, v] of Object.entries(accent)) {
    document.body.style.setProperty(k, v);
  }
}

export function useTweaks(): [Tweaks, <K extends keyof Tweaks>(key: K, value: Tweaks[K]) => void] {
  const [t, setT] = useState<Tweaks>(loadTweaks);

  useEffect(() => {
    applyTweaks(t);
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(t));
    } catch {
      // ignore quota errors
    }
  }, [t]);

  function setTweak<K extends keyof Tweaks>(key: K, value: Tweaks[K]) {
    setT(prev => ({ ...prev, [key]: value }));
  }

  return [t, setTweak];
}

interface PanelProps {
  tweaks: Tweaks;
  setTweak: <K extends keyof Tweaks>(key: K, value: Tweaks[K]) => void;
}

export function TweaksPanel({ tweaks, setTweak }: PanelProps) {
  const [open, setOpen] = useState(false);

  if (!open) {
    return (
      <button
        className="twk-fab"
        aria-label="打开 tweaks 面板"
        title="Tweaks"
        onClick={() => setOpen(true)}
      >
        <Icon name="settings" size={18} />
      </button>
    );
  }

  return (
    <div className="twk-panel" role="dialog" aria-label="Tweaks">
      <div className="twk-hd">
        <b>Tweaks</b>
        <button
          className="twk-x"
          aria-label="关闭"
          onClick={() => setOpen(false)}
        >
          ✕
        </button>
      </div>
      <div className="twk-body">
        <div className="twk-sect">布局</div>
        <div className="twk-row">
          <span className="twk-lbl">卡片密度</span>
          <div className="twk-seg" role="radiogroup" aria-label="卡片密度">
            {(
              [
                ['compact', '紧凑'],
                ['regular', '常规'],
                ['comfy', '宽松'],
              ] as const
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={tweaks.density === v}
                onClick={() => setTweak('density', v)}
              >
                {label}
              </button>
            ))}
          </div>
        </div>

        <div className="twk-sect">主题</div>
        <div className="twk-row">
          <span className="twk-lbl">强调色</span>
          <div className="twk-chips" role="radiogroup" aria-label="强调色">
            {(['orange', 'blue', 'green'] as const).map(a => (
              <button
                key={a}
                type="button"
                role="radio"
                aria-checked={tweaks.accent === a}
                data-on={tweaks.accent === a ? '1' : '0'}
                aria-label={a}
                title={a}
                style={{ background: ACCENT_MAP[a]['--accent'] }}
                onClick={() => setTweak('accent', a)}
              />
            ))}
          </div>
        </div>

        <div className="twk-row twk-row-h">
          <span className="twk-lbl">深色模式</span>
          <button
            type="button"
            className="twk-toggle"
            role="switch"
            aria-checked={tweaks.dark}
            data-on={tweaks.dark ? '1' : '0'}
            onClick={() => setTweak('dark', !tweaks.dark)}
          >
            <i />
          </button>
        </div>
      </div>
    </div>
  );
}
