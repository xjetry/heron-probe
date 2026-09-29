// 公开页的显示选项：两个开关，各自独立，与标签筛选叠加（过滤在前，排序在后，见 Overview）。
export function ViewOptions({ offlineOnly, byExpiry, onOfflineOnly, onByExpiry }: {
  offlineOnly: boolean;
  byExpiry: boolean;
  onOfflineOnly: (on: boolean) => void;
  onByExpiry: (on: boolean) => void;
}) {
  return (
    <div className="view-options" role="group" aria-label="显示选项">
      <button type="button" className="chip" aria-pressed={offlineOnly} onClick={() => onOfflineOnly(!offlineOnly)}>仅离线</button>
      <button type="button" className="chip" aria-pressed={byExpiry} onClick={() => onByExpiry(!byExpiry)}>按到期时间排序</button>
    </div>
  );
}
