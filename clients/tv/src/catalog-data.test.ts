import {describe,expect,it} from 'vitest';
import {CatalogFacets, HouseholdItem, HouseholdState} from '@filelist/shared';
import {householdSections,trackerCategories} from './catalog-data';

const empty={favorites:[],continueWatching:[],recent:[],watched:[]} as HouseholdState;

describe('Tizen route data parity',()=>{
  it('uses the complete facet response for tracker categories',()=>{
    const facets={categories:['Movies 4K','TV-Series HD'],kinds:[],resolutions:[],hdr:[],qualities:[],codecs:[]} as CatalogFacets;
    expect(trackerCategories(facets)).toEqual(facets.categories);
  });
  it('matches the website household sections',()=>{
    expect(householdSections('home',empty).map(section=>section.key)).toEqual(['continue','favorites']);
    expect(householdSections('library',empty).map(section=>section.key)).toEqual(['continue','recent','watched']);
    expect(householdSections('favorites',empty).map(section=>section.key)).toEqual(['favorites']);
  });
  it('renders one household dashboard card per canonical series title',()=>{
    const release={id:'silo-pack',name:'Silo Season 1',category:'TV-Series HD',sizeBytes:40_000_000_000,seeders:20,leechers:1,freeleech:false};
    const episode=(number:number,updatedAt:string)=>({profileId:'household',sourceId:`silo-e${number}`,releaseId:release.id,fileIndex:number,filePath:`Silo.S01E${String(number).padStart(2,'0')}.mkv`,positionMs:1000,durationMs:3600000,watched:false,updatedAt,release,favorite:false,titleId:'silo',seasonNumber:1,episodeNumber:number}) as HouseholdItem;
    const state={...empty,continueWatching:[episode(2,'2026-08-14T10:00:00Z'),episode(3,'2026-08-14T12:00:00Z')]} as HouseholdState;
    const items=householdSections('home',state)[0].items;
    expect(items).toHaveLength(1);
    expect(items[0].episodeNumber).toBe(3);
  });
});
